# 07 · CLI

One binary, `beam`. Every subcommand except `daemon` and `version` is a thin client of
the control socket ([06-control-socket](06-control-socket.md)) and will spawn the
daemon if none is running. Output is for humans by default; `--json` gives the socket
result verbatim. Exit codes: `0` success, `1` a beam error (the token from the socket is
on stderr), `2` usage, and for `exec` the remote exit code.

```
beam init [--label NAME] [--replace-root]
beam join [--label NAME]
beam add <join-address>

beam daemon [--detach] [--derp-map URL]
beam status [--json]
beam peers [--json]
beam peer rename <peer> <label>
beam peer forget <peer>
beam peer grant <peer> <pty,exec,msg | all | none>
beam revoke <peer>

beam connect <peer> [--cwd PATH] [-- argv...]
beam exec <peer> [--cwd PATH] [--env K=V]... -- argv...
beam msg send <peer> [--topic T] [--base64] <payload | ->
beam msg listen [--topic T] [<peer>...]
beam msg queue [<peer>] [--json]

beam version
```

## Enrolment

**`beam init`** creates this machine's key, registers the passkey, attests this machine.
Prints the ceremony URL and tries to open it. Ends with:

```
registered passkey for this fleet
this machine: laptop (b7f3 9a21 0c4e 55d1)
address: tc…
```

`--replace-root` is refused unless `fleet.json` exists and the user confirms; it keeps
`key.json`, `peers.json`, `revocations.json` and writes a new `fleet.json`.

**`beam join`** prints:

```
this machine: buildbox (c5aa 18e0 77b2 0d31)

on a machine that has your passkey, run:

  beam add tc…

or scan:
  <QR code>

waiting (expires in 10:00)…
```

then, on success, `joined: added by laptop; 3 machines pinned`. On failure, the failed
check by name and exit 1.

**`beam add <address>`** connects, prints
`add "buildbox" (c5aa 18e0 77b2 0d31) to your fleet? [y/N]`, on `y` runs the ceremony
(prints and opens the URL), and ends with `added "buildbox"; connected (direct)`.
Refuses with `not enrolled — run "beam init" first` or `"…" was revoked; wipe its
$BEAM_DIR to give it a new identity`.

## Daemon

**`beam daemon`** runs in the foreground, logs to stderr. `--detach` forks and returns
once the socket is ready. `--derp-map URL` overrides the DERP map. Exits 1 immediately
if another daemon holds the lock.

## Status and peers

**`beam status`**:

```
this machine: laptop (b7f3 9a21 0c4e 55d1)
fleet:        enrolled
daemon:       running, 2 of 3 peers connected
derp:         fra (tailcat.dev)
address:      tc…
```

**`beam peers`**:

```
LABEL      PEER ID              STATE      PATH        GRANTS   QUEUED
buildbox   c5aa18e077b20d31     connected  direct      all      0
homebox    7b04e2…              connected  relay:fra   msg      2
oldlaptop  a3…                  revoked    -           -        0
```

`beam peer grant` takes a comma list, `all` (clears the field) or `none` (`[]`).

**`beam revoke <peer>`** prints `revoked "oldlaptop"; it will be dropped by other
machines as they next connect`.

## Streams

**`beam connect <peer>`** opens a `pty` with the current terminal's size, forwards
`SIGWINCH` as resize, puts the local terminal in raw mode, and exits with the remote
process's exit code. With no `argv` the remote login shell runs.

**`beam exec <peer> -- argv...`** pipes stdin, writes stdout and stderr to the local
ones, and exits with the remote exit code (or 128+signal). `--cwd` is passed through
for the acceptor to resolve. `--env K=V` repeats.

## Messages

**`beam msg send <peer> <payload>`** reads `-` from stdin. Prints one of:

```
delivered to buildbox
queued for buildbox — that machine is not connected right now. beam will deliver this
message when it comes online. Do not send it again.
rejected: unknown-peer
```

Exit 0 for `delivered` and `queued`, 1 for `rejected`.

**`beam msg listen`** subscribes and prints one JSON envelope per line, acking each
after the line is written. `--topic` and peer filters are applied by the daemon.

**`beam msg queue`** lists outbound, inbound and quarantined envelopes with `seq`,
`topic`, age and, for quarantined, the reason.

## Peer arguments

`<peer>` is a `peerId`, a unique prefix of one (≥ 8 hex characters), or a label. An
ambiguous prefix or label is an error naming the candidates.

## What the CLI does not have

- `serve`, `pair`, `--hostname`, `--tailscale-serve`, `node`: the daemon is implicit and
  there is nothing to bind.
- `enroll`: replaced by `init` / `join` / `add`.
- Any command that edits `$BEAM_DIR` files when the daemon is not running. Everything
  goes through the socket, which spawns the daemon.
