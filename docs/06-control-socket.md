# 06 · The control socket

The daemon's one local interface: `$BEAM_DIR/run/beam.sock`, mode `0600`. (The tunnel
port is the only other thing it listens on; a ceremony's result comes back through the
worker, [02](02-identity.md).) Override with `BEAM_SOCKET`, which is also what the
daemon injects into remote processes; `BEAM_CONFIG_DIR` selects the directory and
therefore the default path. A path longer than a Unix socket address holds (107 bytes on
Linux, 103 on macOS) is refused before anything else, naming `BEAM_SOCKET`.

## Lifecycle

- The daemon takes an exclusive lock on `run/beam.lock` first; a second daemon exits 1
  with `another daemon holds $BEAM_DIR`. A socket file with no listener is removed after
  a failed connect.
- **Unenrolled state.** With no `fleet.json` the daemon serves the socket and answers
  `status`, `events.subscribe`, the ceremony ops, `fleet.reset` and `daemon.shutdown`;
  everything else is `not-enrolled`.
  The socket is ready before transport starts; `status.ready` reports transport state.
  `status` answers at once; every other op waits until the daemon has started.
- **Connect-or-spawn**, one shared helper used by the CLI and n10's TUI: connect; on
  `ECONNREFUSED`/`ENOENT` run `beam daemon --detach`, wait ≤ 5 s for the socket, connect.
  `--detach` starts `beam daemon` with the same options but `--detach` in a new session,
  its output appended to `$BEAM_DIR/daemon.log`, and returns 0 without waiting for it. A
  daemon that loses the lock race logs `another daemon holds $BEAM_DIR` there and exits
  1; the helper connects to the winner either way.
- **Shutdown.** `daemon.shutdown`, SIGTERM or SIGINT stop the daemon, whoever started
  it; so does the end of its stdin for one started with `--exit-with-parent`
  ([07](07-cli.md)). Stopping, the daemon closes its socket first (an op that fails
  meanwhile gets no reply, since the stop may be why, a ceremony's `*.wait` among them;
  its connection closes when the daemon closes its clients' connections), lets a fleet
  reset or re-join under way finish, then refuses new streams, ends every stream it serves and
  waits up to 10 s for each to tear down ([04](04-streams.md)), so a session's teardown
  completes before the daemon exits. An open `pty` session holds that for its 5 s grace.
  A fleet reset and a re-join end the streams the same way. Clients treat a closed
  socket after `daemon.shutdown` as deliberate and do not respawn until asked; any other
  disconnect is unexpected. n10 recovers from one as [08](08-desktop.md) says; the CLI
  is one call per process and reconnects never.

## Two kinds of connection

**Control**: newline-delimited JSON that escapes neither markup nor U+2028/U+2029, ≤ 1 MiB
per line, `{ id, op, … }` → `{ id, ok, result | error, detail? }`, out-of-order replies
allowed. A request is flat: an op's fields sit beside `id` and `op`, with no params
object. Events are each a line `{ event, data }`: the daemon's after `events.subscribe`,
and `mail`, whose `data` is the envelope, after `msg.subscribe`. A client that leaves a
line unread for 10 s is disconnected.

**Attach**: first line `{ "attach": "<streamId>" }`, then the frame format from
[04](04-streams.md) verbatim in both directions, `taken` included. The daemon relays frames
unchanged, reading the client's from the moment it attaches, and holds the client to the
input window as the peer holds the daemon: a client frame beyond it, or a peer's `taken`
with no client input outstanding, ends the attach with `close {"reason":"window"}`. A client's `close` frame ends it as closing the connection
does.

## Operations

### Daemon

| op | request | result |
|---|---|---|
| `status` | | `{ version, ready, enrolled, peerId, label, fleetId?, address?, derp: { region, source }, peers: { connected, offline, revoked, revokedByFleet } }` |
| `events.subscribe` | | `{}` |
| `daemon.shutdown` | | `{}` then exit |

### Peers

`PeerView`: `{ peerId, label, alias, state, inbound, path, lastSeenAt, grant, revokedAt,
pinnedAt, queue: { outbound, inbound, refused } }` (counts, not lists).

| op | request | result |
|---|---|---|
| `peers` | `{ cursor?, limit? ≤ 200 }` | `{ peers: PeerView[], next? }` |
| `peer.resolve` | `{ peer }` | `{ peerId }`; `peer` is a full id, a ≥ 8-char hex prefix, or an alias/label; hex-looking input is tried as prefix first; `ambiguous-peer` lists candidates in `detail` |
| `peer.alias` | `{ peer, alias \| null }` | `{}` |
| `peer.grant` | `{ peer, grant: "all" \| "msg" \| "none" }` | `{}`; an open stream from that peer outside the new grant is terminated |

### Ceremonies

Every ceremony op returns a `ceremonyUrl`, the whole URL the owner's browser opens,
fragment included: a client opens it, prints it, draws it as a QR code for a phone to
scan, or all three ([07](07-cli.md), [08](08-desktop.md)). The matching `*.wait` blocks
while the daemon waits on the ceremony's slot. One ceremony at a time (`busy`).

| op | request | result |
|---|---|---|
| `init.start` | `{ label, fleetName }` | `{ ceremonyUrl }` — the `create` |
| `init.wait` | | `{ ceremonyUrl }` for the `get`, as an event `ceremony`, then `{ peerId, fleetId, address, published: true \| "pending" }` |
| `join.start` | `{ label }` | `{ ceremonyUrl }` |
| `join.wait` | | `{ peerId, fleetId, members: n, published: true \| "pending" }` |
| `revoke.start` | `{ peer }` | `{ ceremonyUrl }` |
| `revoke.wait` | | `{ local: true, published: true \| "pending", acknowledgedBy: n }`; `n` as [02](02-identity.md) defines it |
| `ceremony.cancel` | | `{}` |
| `fleet.reset` | `{ confirm: "reset" }` | `{}` |

While a `*.wait` runs, its client also gets `stage { stage }` events as the daemon
reaches `reading directory`, `notifying peers` and `publishing` ([07](07-cli.md)). A `*.wait` without its
`*.start` under way is `ceremony-state`, and so is a slot answered with a result that
does not open under the ceremony's key ([02](02-identity.md)). `published: "pending"`
means the directory append is queued in `state.db` and retried (a write the worker
refuses outright is dropped from the queue and logged); event `directory.published {
kind, peerId }` fires when it lands. `join` fails outright with `directory-unavailable`
because it cannot proceed without the read.

### Messages

| op | request | result |
|---|---|---|
| `msg.send` | `{ to, topic, payload, encoding }` | `{ outcome, to, pendingReason?, reason? }` |
| `msg.subscribe` | `{ topic?, from? }` (`from`: peer arguments) | `{}`; then `mail` events |
| `msg.ack` | `{ envelopeId }` | `{}` |
| `msg.defer` | `{ envelopeId, reason ≤ 1 KiB }` | `{}` |
| `msg.queue` | `{ peer?, which: "outbound" \| "inbound" \| "refused" \| "quarantine", cursor?, limit? ≤ 100 }` | `{ items: [{ envelope, reason? }], next? }` |

`msg.ack` and `msg.defer` name the subscriber's in-flight envelope by its `id` as
`envelopeId`; any other is `params`. A second `msg.subscribe` on a connection replaces its
subscription, releasing what it held. `refused` lists deferred inbound envelopes with the
defer's reason, and `quarantine` outbound ones the recipient refused for good, with its.
A `msg.queue` page also stops before an item that would take its line past 1 MiB.

### Streams

| op | request | result |
|---|---|---|
| `pty.open` | `{ peer, argv?, cwd?, env?, cols, rows }` | `{ streamId }` — a reservation; nothing is sent to the peer until the attach connection arrives |
| `exec.open` | `{ peer, argv, cwd?, env? }` | `{ streamId }` — same |
| `stream.close` | `{ streamId }` | `{}` |

On attach the daemon dials the peer if needed (bounded 20 s; `offline` on failure is
delivered as a `close` frame on the attach connection), opens the remote stream, and
pumps. A reservation not attached within 10 s expires; nothing ran remotely, and an
attach for it (or any unknown `streamId`) gets `close {"reason":"params"}`.

Every end of an attached stream reaches the client as a `close` frame and subscribers as
`stream.closed`: the peer's own `close` (`exit`, `window`, or a refusal reason from
[04](04-streams.md)), `offline`, `window` when the client overruns its input window or the
peer answers input that was not sent, or
`connection-lost` when the peer's stream ends without
one, or when this daemon stops. `stream.close`, or the client closing its connection,
ends it as `detached` at any point: it closes the remote side, and before the remote stream
is open nothing is sent to the peer.

### Events

`peer { PeerView }` · `peer.new { PeerView }` · `mail { envelope }` (acked by its `id` as
`envelopeId`) · `stream.closed {
streamId, reason, exitCode?, signal? }` · `ceremony { ceremonyUrl }` ·
`directory.published { kind, peerId }`.

## Errors

`not-enrolled` `already-enrolled` `unknown-peer` `ambiguous-peer` `revoked-peer`
`grant` `limit` `params` `offline` `spawn` `bad-entry` `wrong-passkey` `bad-assertion`
`ceremony-timeout` `ceremony-state` `ceremony-cancelled` `prf-unsupported`
`directory-unavailable` `queue-full` `storage-failure` `busy` `internal`.
`revoked-peer` on the socket corresponds to `revoked` in entry verification and stream
refusal; the mapping is one to one and listed here once.
