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

Each ceremony draws its URL as a QR code when stdout is a terminal, prints the URL under
it whether or not, and opens the browser when the machine has one (a display, and not an
SSH session). The owner finishes in whichever they like: the browser that opened, or a
phone that scanned the code. Nothing else differs between a desktop and a headless
machine. Output shows stages: `starting daemon`, `preparing network`, `waiting for your
passkey (create)`, `waiting for your passkey (sign)`, `reading directory`, `publishing`.

`beam init` ends:

```
created fleet 3f9a 0c4e 7d12 e805  ·  this machine: laptop (b7f3 9a21 0c4e 55d1)
published to directory
```

`beam join` ends `joined fleet 3f9a 0c4e 7d12 e805; 3 other machines known; connecting…`
and returns; `beam peers` shows progress. With the directory down: `directory
unavailable; try again`. The fleet is its fingerprint, 64 bits of `fleetId` in the form
`init` and `beam status` print too. Compare it with `beam status` on a machine already
in the fleet: a different one means this machine joined another fleet, through a
substituted ceremony page and a directory that went along ([01](01-model.md)), and
`beam fleet reset` undoes that.

`beam revoke` ends:

```
revoked "oldlaptop" on this machine
published to directory            | publication pending; will retry
acknowledged by 2 of 3 peers      (offline peers learn when they connect)
```

`beam fleet reset` asks `type "reset" to confirm`.

A ceremony shows:

```
waiting for your passkey (sign)
⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿
⣿⡏⠉⠉⠉⢹… (the QR code, 28 × 14 for a typical URL)
https://beam.n10.is/#c=…&f=b7f39a210c4e55d1&k=…&l=buildbox&o=a&s=…
scan with your phone or open the link; approve only a page that shows what you ran
```

The QR code encodes the URL byte for byte at error correction level L. Each character
cell is a braille pattern (U+2800–U+28FF) holding 2 × 4 modules, a light module a raised
dot, drawn white on black by SGR whatever the terminal's theme, with a three-module
quiet zone. A typical URL is version 8, 55 modules with the quiet zone, 28 columns by 14
rows; a 64-character ASCII label is version 9 (30 × 15), and `init` with a 64-character
label and fleet name version 11 (34 × 17). A non-ASCII label, which percent-encoding
triples, makes it larger. The URL below it is the fallback: it is printed on every
output, a terminal's or a pipe's. The page on the phone checks nothing against the
terminal: the owner compares the action, machine and fingerprint it shows with what they
ran ([01](01-model.md), The relayed result).

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
