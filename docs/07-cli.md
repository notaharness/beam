# 07 · CLI

One binary, `beam`. Every subcommand except `daemon` and `version` is a thin client of
the control socket ([06-control-socket](06-control-socket.md)) and will spawn the
daemon if none is running. Output is for humans by default; `--json` gives the socket
result verbatim. Exit codes: `0` success, `1` a beam error (the token from the socket is
on stderr), `2` usage, and for `exec` the remote exit code.

```
beam init [--label NAME]
beam join [--label NAME]

beam daemon [--detach] [--derp-map URL]
beam status [--json]
beam peers [--json]
beam peer rename <peer> <label>
beam peer forget <peer>
beam peer grant <peer> <pty,exec,msg | all | none>
beam revoke <peer>                      one passkey tap

beam connect <peer> [--cwd PATH] [-- argv...]
beam exec <peer> [--cwd PATH] [--env K=V]... -- argv...
beam msg send <peer> [--topic T] [--base64] <payload | ->
beam msg listen [--topic T] [<peer>...]
beam msg queue [<peer>] [--json]

beam version
```

## Enrolment

**`beam init`** creates this machine's key, creates the passkey, derives the fleet keys,
signs this machine's entry and appends it to the directory. Prints the ceremony URL and
tries to open it. Ends with:

```
created fleet 3f9a…
this machine: laptop (b7f3 9a21 0c4e 55d1)
```

Refuses with `already in a fleet — wipe $BEAM_DIR/fleet.json to start another` if
`fleet.json` exists.

**`beam join`** derives the fleet keys from the existing passkey, reads the directory,
signs this machine's entry and appends it:

```
open this in a browser that has your passkey (or forward the port):

  https://pair.n10.is/#…

waiting…
joined fleet 3f9a…; 3 machines known
```

Then the daemon starts and each known machine admits this one as it is reached.

**`beam revoke <peer>`** opens a ceremony showing "Remove oldlaptop from your fleet",
and ends with `revoked "oldlaptop"; other machines will refuse it as they next connect`.

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

- Anything to bind, serve or pair. The daemon is implicit; joining is a tap.
- An `add` on an existing machine. Membership is minted by the passkey, on the machine
  joining.
- Any command that edits `$BEAM_DIR` files when the daemon is not running. Everything
  goes through the socket, which spawns the daemon.
