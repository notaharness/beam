# 07 · CLI

One binary. Every subcommand but `daemon` and `version` is a client of the control socket
and uses the shared connect-or-spawn helper. `status`, `peers` and `msg send` take
`--json`, which prints the socket result as it is, the exit code unchanged. Exit
codes: `0`; `1` beam error (token on stderr); `2` usage; `connect` and `exec` exit with
the remote status (`128+signal` for a signal).

```
beam init   [--label NAME] [--fleet-name NAME]
beam join   [--label NAME]
beam revoke <peer>
beam fleet reset

beam daemon [--detach] [--derp-map URL]
beam status [--json]
beam peers  [--json]
beam peer alias <peer> <alias|->
beam peer grant <peer> all|msg|none

beam connect <peer> [--cwd PATH] [-- argv...]
beam exec    <peer> [--cwd PATH] [--env K=V]... -- argv...
beam msg send   <peer> [--topic T] [--base64] [--json] <payload|->
beam msg listen [--topic T] [<peer>...]
beam msg queue  [<peer>] [--which outbound|inbound|refused|quarantine]

beam version
```

## Enrolment

Each ceremony prints its URL and opens the browser when one is available. Output shows
stages: `starting daemon`, `preparing network`, `waiting for your passkey (create)`,
`waiting for your passkey (sign)`, `reading directory`, `publishing`.

`beam init` ends:

```
created fleet 3f9a…  ·  this machine: laptop (b7f3 9a21 0c4e 55d1)
published to directory
```

`beam join` ends `joined fleet 3f9a…; 3 other machines known; connecting…` and returns;
`beam peers` shows progress. With the directory down: `directory unavailable; try again`.

`beam revoke` ends:

```
revoked "oldlaptop" on this machine
published to directory            | publication pending; will retry
acknowledged by 2 of 3 peers      (offline peers learn when they connect)
```

`beam fleet reset` asks `type "reset" to confirm`.

On a headless machine a ceremony prints the URL and:
`forward the port first: ssh -L 7xxx:127.0.0.1:7xxx <this machine>`.

## Streams

`connect` puts the terminal in raw mode, forwards `SIGWINCH`, restores the terminal on
every exit path, shows `connecting to homebox…` while dialing, and distinguishes `remote
shell exited (0)`, `killed by SIGKILL`, `connection lost`.

`exec` pipes stdin, streams stdout and stderr, exits with the remote status.

## Messages

`msg send` prints `delivered to buildbox`, or the `stored` sentence from
[05](05-mailbox.md) for its `pendingReason`, or `rejected: <reason>` on stderr. Exit 0
for the first two. The payload is the argument, or stdin for `-`; `--base64` sends it as
bytes.

`msg listen` prints one envelope per line and acks after the write to stdout succeeds.
That is the durability an observer gets; a consumer that needs more writes its own.
Peers given filter by sender.

`msg queue` prints one `{ envelope, reason? }` per line from `msg.queue`, all pages,
`outbound` unless `--which` says otherwise.

## Peer arguments

Resolved by the daemon (`peer.resolve`): full id, ≥ 8-hex prefix, alias, or label. The
error for ambiguity lists candidates.
