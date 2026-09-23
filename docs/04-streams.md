# 04 · Streams

A stream is one TCP connection through a tunnel to the peer's port 7000. It is opened
only by the tunnel's dialer, declared by a one-line header, answered by a one-line
response, and then carries frames.

## Header and response

First line, ≤ 64 KiB, newline-terminated JSON:

```json
{ "v": 1, "kind": "hello" }
{ "v": 1, "kind": "sync" }
{ "v": 1, "kind": "pty",  "argv": [...], "cwd": "~/work", "env": {...}, "cols": 220, "rows": 50 }
{ "v": 1, "kind": "exec", "argv": ["git","status"], "cwd": "/srv/repo" }
{ "v": 1, "kind": "msg" }
```

Response: `{ "ok": true }` or `{ "ok": false, "reason": "<token>", "detail"?: "…" }` then
close. Reasons: `version`, `kind`, `unauthenticated` (tunnel not yet through hello),
`revoked`, `grant`, `limit`, `params`, `spawn`.

## Frames

```
type   u8      0 data · 1 control (JSON) · 2 close (JSON)
length u32be   payload bytes, ≤ 1 MiB
payload
```

`close` carries `{ "reason": … }`; the sender half-closes after it. A reset without
`close` is abnormal and reported as such.

## `hello`

The first stream on every tunnel. One data frame from the dialer:
`{ "entry": <member entry>, "nonce": "…", "mac": "…" }` ([03](03-transport.md),
Admission), `nonce` 32 random bytes and `mac` 32 bytes, both unpadded base64url like
every binary field. One data frame back: `{ "ok": true }` or `{ "ok": false, "reason":
"bad-entry" | "wrong-passkey" | "bad-assertion" | "revoked" | "possession" }`, then close.

## `sync`

Opened by the dialer after hello; lives as long as the tunnel. Data frames carry
`{ "records": [ <entry or revocation>, … ] }`, ≤ 200 records and ≤ 1 MiB each; a
`control {"kind":"end"}` marks the end of the initial dump; later frames are deltas.
`control {"kind":"ping","t":…}` / `{"kind":"pong","t":…}` carry liveness. Not subject
to grants.

## `pty`

`argv?`, `cwd?`, `env?`, `cols`, `rows`. Empty `argv` runs the login shell: `$SHELL` if it
names an executable, else `/bin/sh`, invoked as `-<basename>`. Otherwise `argv[0]`
directly, no shell. `cwd` absolute or `~/`-relative, resolved on the acceptor. `env`
merged over the daemon's environment; injected variables last. Unix: `creack/pty`.

Control from the opener: `{"kind":"resize","cols":…,"rows":…}` (2–500). Data both ways
raw. Process exit → `close {"reason":"exit","exitCode":n,"signal"?:s}`; a process ended
by a signal has `exitCode` 128+signal and `signal` its name (`SIGKILL`). Opener close or
connection loss → SIGHUP then SIGKILL to the process group after 5 s, whether or not its
leader has already exited.

## `exec`

`argv` (required), `cwd?`, `env?`. Data payloads carry a channel byte: `0` stdin
(opener→acceptor), `1` stdout, `2` stderr. `control {"kind":"stdin-eof"}` ends stdin.
Exit as for `pty`, after stdout and stderr drain. Opener close or connection loss closes
stdin and kills the process group.

For both, the process group ends with the stream: descendants still running after their
leader exited get the same teardown once the opener closes (after `close "exit"` or not).
A process meant to outlive the stream starts its own session.

**Input.** On `pty` and `exec` the opener's data and control frames are input, and at most
4 of them may be outstanding: sent and not yet answered by the acceptor's
`control {"kind":"taken"}`, which it sends for each once it has written it to the process
or acted on it. The acceptor so reads on whatever the process does, and the opener's close
is never queued behind input. A frame beyond the window is a protocol error: the stream
ends as on the opener's close, with `close {"reason":"window"}`. Nothing else is
acknowledged.

## `msg`

Opened by the dialer; carries mail **from the dialer to the acceptor only**. One per
tunnel, opened when the outbound queue is non-empty and kept while it drains. Each data
frame is one envelope; the acceptor answers each with
`control {"kind":"ack","id":…,"accepted":true}` or
`{…,"accepted":false,"reason":"duplicate"|"queue-full"|"payload-too-large"|"invalid-envelope"|"storage-failure"}`.
The acceptor requires `envelope.from == the peerId bound to this tunnel` and
`envelope.to == its own peerId`; a mismatch is `invalid-envelope`.

## Grants

Each peer row has `Grant`, what *this* machine lets that peer open here:

| Grant | `pty` / `exec` | `msg` | `hello` / `sync` |
|---|---|---|---|
| `all` (default) | yes | yes | yes |
| `msg` | no | yes | yes |
| `none` | no | no | yes |

`pty` and `exec` are one privilege (a shell as the daemon's user) and are never granted
separately. Read per open. A row whose grant cannot be parsed is `none`. Grants are local
policy on the granting machine and travel nowhere.

## Injected environment

| Variable | Value |
|---|---|
| `BEAM_DIR` | daemon config directory |
| `BEAM_SOCKET` | control socket path |
| `BEAM_PEER_ID` | this machine's `peerId` |
| `BEAM_CALLER_ID` | the opener's `peerId` |
| `BEAM_CALLER_LABEL` | the opener's label or local alias |

## Limits

Header 64 KiB · frame payload 1 MiB · `pty`+`exec` 32 per peer · `argv` ≤ 1,024 items,
each ≤ 64 KiB · `env` ≤ 256 · `sync` records/frame 200 · unbound tunnels in hello 16 ·
`pty`/`exec` input window 4 frames.
