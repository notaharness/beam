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

beam daemon [--detach | --exit-with-parent] [--derp-map URL]
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
passkey (create)`, `waiting for your passkey (sign)`, `reading directory`, `notifying
peers` (revoke, while peers acknowledge), `publishing`.

`beam init` ends:

```
created fleet 3f9a 0c4e 7d12 e805  ·  this machine: laptop (b7f3 9a21 0c4e 55d1)
Published to directory
```

`beam join` ends `joined fleet 3f9a 0c4e 7d12 e805; 3 other machines known; connecting…`
and the publication line, and returns; `beam peers` shows progress. The fleet is its
fingerprint, 64 bits of `fleetId` in the form `init` and `beam status` print too.
Compare it with `beam status` on a machine already in the fleet: a different one means
this machine joined another fleet, through a substituted ceremony page and a directory
that went along ([01](01-model.md)), and `beam fleet reset` undoes that.

`beam revoke` ends:

```
revoked "oldlaptop" on this machine
Published to directory
acknowledged by 2 of 3 peers      (offline peers learn when they connect)
```

The publication line is `Published to directory`, or while the append is queued
([06](06-control-socket.md)) `Saved on this machine. Directory publication is pending;
beam will retry while it runs.`

`beam fleet reset` asks `type "reset" to confirm`.

A ceremony shows:

```
waiting for your passkey (sign)
Action               Add “buildbox” to fleet
Machine              buildbox
Machine fingerprint  b7f3 9a21 0c4e 55d1
⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿
⣿⡏⠉⠉⠉⢹… (the QR code, 28 × 14 for a typical URL)
https://beam.n10.is/#c=…&f=b7f39a210c4e55d1&k=…&l=buildbox&o=a&s=…
scan with your phone or open the link; approve only a page that shows what you ran
```

Action, Machine and Machine fingerprint are read from the URL's fragment and are what
the page shows for it, the Action in the page's words ([02](02-identity.md),
Ceremonies): `Create fleet passkey for “{n}”`, `Add “{l}” to fleet`, `Remove “{l}” from
fleet`.

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

### Ceremony errors

`init`, `join` and `revoke` print a ceremony's error as every command does, its token
and any detail on the first line of stderr (`prf-unsupported: …`), then what it means.
The daemon's detail for `prf-unsupported` names the field the passkey's result lacked:
`prf.enabled` from a `create`, a 32-byte `prf.results.first` from a `get`. A token not
in the table reads as `internal`. An error that is no token, the connection to the
daemon lost during the wait, reads as the last row.

| Token | Explanation |
|---|---|
| `prf-unsupported` | The selected passkey did not provide WebAuthn PRF. beam needs this extension to derive the encrypted fleet directory key. Browser, operating system and passkey provider must all support it. |
| `ceremony-cancelled` | Passkey request cancelled. No further approval is pending for this request. |
| `ceremony-timeout` | This passkey request expired after five minutes. Start again to get a new link and QR code. |
| `ceremony-state` | beam could not use this ceremony result. The request may be stale, already consumed, or answered with data it cannot decrypt. Check this machine’s fleet status (beam status), then start again with a fresh link. |
| `bad-assertion` | The passkey request failed or its answer could not be verified. Check the browser’s message, then start again. |
| `directory-unavailable` | Cannot read the fleet directory. Check the connection to beam.n10.is and try joining again. |
| `wrong-passkey` | This passkey does not unlock the expected fleet. Choose the original fleet passkey using a compatible browser and provider. |
| `busy` | Another passkey request is already running. Finish or cancel it where you started it, then try again. |
| `already-enrolled` | This machine already belongs to a fleet. Run beam status to inspect it. |
| `not-enrolled` | This machine is not in a fleet. Create or join a fleet first (beam init or beam join). |
| `revoked-peer` | This machine identity has been revoked. Resetting its fleet will not make that identity eligible to rejoin. |
| `unknown-peer` | This machine is no longer in the local peer list. Check beam peers before trying again. |
| `ambiguous-peer` | More than one machine matches. Select a machine by its fingerprint. |
| `params` | beam rejected these details. Check the machine and fleet names. |
| `bad-entry` | beam rejected an invalid membership record. Check the details before trying again. |
| `storage-failure` | beam could not save fleet data. Check available disk space and permissions, then retry. |
| `internal` | beam could not complete this request. Check the details and this machine’s fleet status before retrying. |
| connection lost | The connection to beam was interrupted. Check beam status before retrying; the request may have completed. |

`prf-unsupported` goes on, for `join` and `revoke`, `Use the same fleet passkey; a new
passkey creates a different fleet.`, and then for every command lists what the page and
the passkey need, from what vendors document ([02](02-identity.md)), not a tested
matrix:

```
beam needs WebAuthn PRF from the browser, the operating system and the passkey provider together, and X25519 in Web Crypto to seal the page's answer:
  Safari 18.4+ (iOS and iPadOS 18.4+): both; Safari 18.0 to 18.3 has PRF without X25519
  Chrome 133+: X25519; PRF depends on the provider (Google Password Manager: unverified)
  Firefox 139+ on desktop: both; Firefox 130 to 138 has X25519 alone (Android, iOS: unverified)
  1Password for iOS 8.10.74+ returns PRF on iOS 18
  security keys: WebAuthn PRF, not hmac-secret alone (firmware minimums: unverified)
  unverified: Windows Hello, Bitwarden, Proton Pass, Edge, Samsung Internet
```

## The daemon

`beam daemon` runs in the foreground until `daemon.shutdown`, SIGTERM or SIGINT;
`--detach` is connect-or-spawn's ([06](06-control-socket.md)). It first marks every
descriptor it inherited above stderr close-on-exec, so no process it starts, for a peer
or as the detached daemon, inherits one from whatever started beam.

`--exit-with-parent` ties the daemon to the process that started it, through its stdin:

- stdin must be a pipe or a socket. A terminal, a file or `/dev/null` is refused before
  the daemon starts: usage error, exit 2. So is `--detach` with it.
- The daemon reads stdin and discards what it reads. At end of file, or a read error,
  it shuts down as it does on SIGTERM: streams end, the lock and socket are released;
  then it logs `stopped: stdin ended: the parent has exited` and exits 0. SIGTERM and
  SIGINT during that shutdown are handled as ever, not fatal.
- The daemon logs to stderr. A parent that gives it pipes for stdout or stderr drains
  them while it runs; once the parent is gone, a write to them fails and is dropped
  (the daemon ignores SIGPIPE), so the shutdown still completes.
- The parent holds the write end and never writes to it. When the parent exits, however
  it exits (quits, crashes, is killed), the kernel closes its descriptors, and the
  daemon sees end of file, on Linux and macOS alike (D53).
- The write end must stay in the parent alone: a process that inherits it keeps the
  daemon alive.

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
