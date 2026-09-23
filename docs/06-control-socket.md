# 06 · The control socket

The daemon's one local interface: `$BEAM_DIR/run/beam.sock`, mode `0600`. (The tunnel
port and the transient ceremony loopback listener are the other two things it listens
on.) Override with `BEAM_SOCKET`, which is also what the daemon injects into remote
processes; `BEAM_CONFIG_DIR` selects the directory and therefore the default path.

## Lifecycle

- The daemon takes an exclusive lock on `run/beam.lock` first; a second daemon exits 1
  with `another daemon holds $BEAM_DIR`. A socket file with no listener is removed after
  a failed connect.
- **Unenrolled state.** With no `fleet.json` the daemon serves the socket and answers
  `status`, the ceremony ops and `daemon.shutdown`; everything else is `not-enrolled`.
  The socket is ready before transport starts; `status.ready` reports transport state.
- **Connect-or-spawn**, one shared helper used by the CLI and the desktop: connect; on
  `ECONNREFUSED`/`ENOENT` run `beam daemon --detach`, wait ≤ 5 s for the socket, connect.
  A spawn that loses the lock race exits 1 and the helper simply connects to the winner.
  `--detach` starts the daemon in a new session, its output appended to
  `$BEAM_DIR/daemon.log`, and returns.
- **Shutdown.** Only an explicit `daemon.shutdown` or SIGTERM stops the daemon, whoever
  started it. Clients treat a closed socket after `daemon.shutdown` as deliberate and do
  not respawn until asked; any other disconnect is unexpected and the helper reconnects
  with backoff (500 ms → 30 s).

## Two kinds of connection

**Control**: newline-delimited JSON, ≤ 1 MiB per line, `{ id, op, … }` → `{ id, ok,
result | error, detail? }`, out-of-order replies allowed, events (each a line `{ event,
data }`) only after `events.subscribe`.

**Attach**: first line `{ "attach": "<streamId>" }`, then the frame format from
[04](04-streams.md) verbatim in both directions. The daemon is a byte pump.

## Operations

### Daemon

| op | request | result |
|---|---|---|
| `status` | | `{ version, ready, enrolled, peerId, label, fleetId?, address?, derp: { region, source }, peers: { connected, offline, revoked } }` |
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

Every ceremony op returns a `ceremonyUrl` the client opens or prints; the matching
`*.wait` blocks. One ceremony at a time (`busy`).

| op | request | result |
|---|---|---|
| `init.start` | `{ label, fleetName }` | `{ ceremonyUrl }` — the `create` |
| `init.wait` | | `{ ceremonyUrl }` for the `get`, as an event `ceremony`, then `{ peerId, fleetId, address, published: true \| "pending" }` |
| `join.start` | `{ label }` | `{ ceremonyUrl }` |
| `join.wait` | | `{ peerId, fleetId, members: n, published: true \| "pending" }` |
| `revoke.start` | `{ peer }` | `{ ceremonyUrl }` |
| `revoke.wait` | | `{ local: true, published: true \| "pending", acknowledgedBy: n }` |
| `ceremony.cancel` | | `{}` |
| `fleet.reset` | `{ confirm: "reset" }` | `{}` |

`published: "pending"` means the directory append is queued in `state.db` and retried;
event `directory.published { kind, peerId }` fires when it lands. `join` fails outright
with `directory-unavailable` because it cannot proceed without the read.

### Messages

| op | request | result |
|---|---|---|
| `msg.send` | `{ to, topic, payload, encoding }` | `{ outcome, to, pendingReason?, reason? }` |
| `msg.subscribe` | `{ topic?, from? }` | `{}`; then `mail` events |
| `msg.ack` | `{ id }` | `{}` |
| `msg.defer` | `{ id, reason }` | `{}` |
| `msg.queue` | `{ peer?, which: "outbound" \| "inbound" \| "refused" \| "quarantine", cursor?, limit? ≤ 100 }` | `{ items, next? }` |

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
`stream.closed`: the peer's own `close` (`exit`, or a refusal reason from
[04](04-streams.md)), `offline`, or `connection-lost` when the peer's stream ends without
one. `stream.close`, or the client closing its connection, ends it as `detached` and closes
the remote side.

### Events

`peer { PeerView }` · `peer.new { PeerView }` · `mail { envelope }` · `stream.closed {
streamId, reason, exitCode?, signal? }` · `ceremony { ceremonyUrl }` ·
`directory.published { kind, peerId }`.

## Errors

`not-enrolled` `already-enrolled` `unknown-peer` `ambiguous-peer` `revoked-peer`
`grant` `limit` `params` `offline` `spawn` `bad-entry` `wrong-passkey` `bad-assertion`
`possession` `ceremony-timeout` `ceremony-state` `ceremony-cancelled` `prf-unsupported`
`directory-unavailable` `queue-full` `storage-failure` `busy` `internal`.
`revoked-peer` on the socket corresponds to `revoked` in entry verification and stream
refusal; the mapping is one to one and listed here once.
