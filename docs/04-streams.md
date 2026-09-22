# 04 · Streams

A stream is one TCP connection through a tunnel to the peer's port `7000`. The
connection is authenticated before any byte is read, declared by a one-line header,
answered by a one-line response, and then carries frames until either side closes.

## Admission, before the header

On accept, the daemon resolves the connection's remote tunnel address to a node key
(see [03-transport](03-transport.md)) and looks it up in the peer table. Not found, or
`Revoked`: close without reading. The check is independent of tailcat's allowlist and
stays even when upstream gains per-connection admission.

## Open header

The first line, at most 64 KiB, newline-terminated JSON:

```json
{ "v": 1, "kind": "pty", "argv": ["tmux","-u","-S","/tmp/s","attach","-t","=x:"],
  "cwd": "~/work", "env": { "TERM": "xterm-256color" }, "cols": 220, "rows": 50 }
{ "v": 1, "kind": "exec", "argv": ["git","status","--porcelain"], "cwd": "/srv/repo" }
{ "v": 1, "kind": "msg" }
{ "v": 1, "kind": "sync" }
```

Response, one line:

```json
{ "ok": true }
{ "ok": false, "reason": "scope" }
```

Refusal reasons: `version` (unknown `v`), `kind` (unknown kind), `scope` (the peer's
grant does not include this kind), `limit` (per-peer cap reached), `params` (a required
field missing or malformed, including a `cwd` that is neither absolute nor `~/`-relative
and a `cols`/`rows` outside 2–500), `spawn` (the process could not be started; the
detail follows in the same object as `detail`). After `ok: false` the acceptor closes.

## Frames

After the response, both directions carry frames:

```
offset  len  field
0       1    type      0 = data, 1 = control, 2 = close
1       4    length    u32 big-endian, payload bytes, ≤ 1 MiB
5       n    payload
```

- `data`: bytes for the stream's purpose. On `exec`, the first payload byte is a channel:
  `0` stdin (opener → acceptor), `1` stdout, `2` stderr (acceptor → opener).
- `control`: JSON, kind-specific, below.
- `close`: JSON `{ "reason": … }`, then the sender half-closes. The receiver may still
  flush what it holds, then closes. A TCP reset without a `close` frame is an abnormal
  end and is reported as one.

This is a framing, not a multiplexer: there are no stream ids and no windows. It exists
because `pty` and `exec` need an in-band control channel and a typed end, and `msg`
needs an ack per envelope. About fifty lines each side.

## `pty`

Open parameters: `argv?`, `cwd?`, `env?`, `cols`, `rows`. An empty `argv` runs the login
shell (`$SHELL`, else `bash`, else `sh`); otherwise `argv[0]` is executed directly with
no shell and no word splitting. `cwd` is resolved on the acceptor: absolute, or `~/`
expanded against the daemon user's home. `env` entries are merged over the daemon's
environment; the injected variables below are set last and cannot be overridden.

Unix: `github.com/creack/pty` (already a tailcat dependency). Windows: ConPTY via
`CreatePseudoConsole`; tailcat's `tailcat_ssh_windows.go` has a working implementation
to copy.

Control from the opener: `{ "kind": "resize", "cols": 220, "rows": 50 }`, clamped to
2–500. Close from the opener kills the process group. The process exiting sends
`close { "reason": "exit", "exitCode": n, "signal": s|null }`.

Cap: 32 live `pty` streams **per peer**. A peer that fills its allowance shrinks no
other peer's; a peer that reconnects finds its own budget intact.

## `exec`

Open parameters: `argv` (required, non-empty), `cwd?`, `env?`. Same execution and `cwd`
rules as `pty`. `argv[0]` runs directly, no shell.

Data frames carry the channel byte. End of stdin is
`control { "kind": "stdin-eof" }`, never a zero-length data frame. Exit is
`close { "reason": "exit", "exitCode": n, "signal": s|null }` from the acceptor after
stdout and stderr are drained. Close from the opener kills the process group.

Cap: 32 live `exec` children per peer, same reasoning as `pty`.

## `msg`

Either side may open one; either side may send. Data frames carry one mailbox envelope
each as JSON ([05-mailbox](05-mailbox.md)). The receiver answers each with
`control { "kind": "ack", "id": "<envelope id>", "accepted": true }` or
`{ …, "accepted": false, "reason": "<ack reason>" }`. The flusher sends one envelope,
waits for its ack, and continues. A `msg` stream stays open for the life of the tunnel;
the daemon opens one per connected peer.

## `sync`

Opened by each side once per tunnel establishment. One data frame each way carrying the
sync document from [02-identity](02-identity.md) (label, directory, revocations), then
`close`. During `join` and `add` the same kind carries the enrolment exchange instead;
the acceptor knows which by whether it is a join server.

## Scopes

A peer record carries which kinds that peer may open here.

| `Scopes` | Meaning |
|---|---|
| `nil` (field absent) | all three: `pty`, `exec`, `msg`. The default. |
| `["msg"]` | exactly that; `pty` and `exec` refused with `scope` |
| `[]` | nothing; the peer authenticates and opens no stream |
| anything unparseable | nothing. A field that exists but cannot be read is not a reason to hand over a shell. |

Read per open, so `beam peer grant` takes effect on a live tunnel without a reconnect.
`sync` is never subject to scopes: a peer with `[]` still exchanges directory and
revocations, because that is how it learns it has been revoked.

There are three names and nothing else: no roles, no wildcards, no filtering of what an
`exec` may run. `pty` and `exec` both grant a shell as the daemon's user; they are
separate only so that a peer can be given neither.

## Injected environment

Every process started for a `pty` or `exec` receives, after the caller's `env`:

| Variable | Value |
|---|---|
| `BEAM_DIR` | the daemon's config directory |
| `BEAM_SOCKET` | path of the control socket |
| `BEAM_PEER_ID` | this machine's `peerId` |
| `BEAM_CALLER_ID` | the opener's `peerId` |
| `BEAM_CALLER_LABEL` | the opener's label as this machine knows it |

A script beam started can therefore answer the machine that started it — `beam msg send
"$BEAM_CALLER_ID" …` — without configuration.

## Limits, collected

| | |
|---|---|
| Header line | 64 KiB |
| Frame payload | 1 MiB |
| `pty` per peer | 32 |
| `exec` per peer | 32 |
| `cols`, `rows` | 2–500 |
| `argv` entries | 1,024; each ≤ 64 KiB |
| `env` entries | 256 |
