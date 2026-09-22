# 06 · The control socket

The daemon exposes one local socket. The CLI, n10 desktop, n10 TUI and Orchestra
scripts all use it; none of them link beam's internals. It is the only interface the
daemon has besides port 7000 through the tunnels.

## Location and lifecycle

- Unix: `$BEAM_DIR/run/beam.sock`, mode `0600`, owned by the daemon's user.
- Windows: named pipe `\\.\pipe\beam-<sha256(BEAM_DIR)[:16]>` with a DACL for the
  current user only.

**One daemon per `$BEAM_DIR`.** The daemon takes an exclusive advisory lock on
`$BEAM_DIR/run/beam.lock` before touching anything else; a second daemon fails fast
with `another daemon holds $BEAM_DIR`. A stale socket file with no listener is removed
after a connect attempt fails.

**Connect-or-spawn.** Every client connects; on `ECONNREFUSED` / `ENOENT` it runs
`beam daemon --detach`, waits for the socket (up to 5 s), and connects again. Two
clients racing to spawn are resolved by the lock: the loser exits 0 and the winner's
socket serves both. A headless machine runs `beam daemon` under systemd or launchd
instead; `beam daemon --install` (later) writes that unit.

**Shutdown.** The daemon outlives its clients. A `daemon.shutdown` request is honoured
only from a local client; the desktop sends it only when the user quits with "also stop
beam". Otherwise closing the app leaves the machine reachable, which is what a listener
is for. SIGTERM: stop accepting, close tunnels politely, flush nothing (queues are
already durable), exit.

## Two kinds of connection

### Control connection

Newline-delimited JSON, one request per line, one response per request, plus
unsolicited events on a connection that asked for them. A line is at most 1 MiB; a
client that exceeds it is disconnected.

```json
→ { "id": 1, "op": "status" }
← { "id": 1, "ok": true, "result": { … } }
← { "id": 2, "ok": false, "error": "unknown-peer" }
← { "event": "peer", "peer": { … } }
```

`id` is caller-chosen and echoed. Requests on one connection may be answered out of
order. Events are sent only after `events.subscribe`.

### Attach connection

A byte stream (`pty` data, `exec` stdio) does not travel base64-encoded on the control
connection; that is head-of-line blocking waiting to happen. Instead, a stream open on
the control connection returns a `streamId`, and the client opens a **second**
connection to the same socket whose first line is

```json
{ "attach": "<streamId>" }
```

After that the connection carries the frame format from [04-streams](04-streams.md)
verbatim, both ways: what the daemon receives from the peer is written to the attach
connection, what the client writes is forwarded to the peer. The daemon is a byte pump
between two framed connections. An unattached `streamId` is cancelled after 10 s.

## Operations

### Daemon and status

| op | request | result |
|---|---|---|
| `status` | | `{ peerId, label, version, fleet: bool, address, derp: { region, mapSource }, peers: PeerView[] }` |
| `events.subscribe` | | `{}`; this connection now receives events |
| `daemon.shutdown` | | `{}` then the daemon exits |

`PeerView` is `{ peerId, label, state, path, lastSeenAt, queueDepth, inboundWaiting,
scopes, revokedAt }` where `state ∈ connected | offline | revoked` and
`path ∈ "direct" | "relay:<region>" | null`.

### Peers

| op | request | result |
|---|---|---|
| `peers` | | `PeerView[]` |
| `peer.rename` | `{ peer, label }` | `{ label }` (as resolved after collision suffixing) |
| `peer.forget` | `{ peer }` | `{}` |
| `peer.grant` | `{ peer, scopes: ["msg"] \| null }` | `{}` |
| `peer.revoke` | `{ peer }` | `{}`; the tunnel is terminated before the response |

`peer` accepts a `peerId` or a label; a label that matches nothing or more than one
record is `unknown-peer` / `ambiguous-peer`.

### Enrolment

The daemon owns keys and verification; the client owns the screen and the browser.

| op | request | result |
|---|---|---|
| `init.start` | `{ label }` | `{ ceremonyUrl }`; the client opens it. The daemon has opened the loopback listener. |
| `init.wait` | | blocks until the `create` result arrives and is verified, then runs the attestation `get` (returns a second `ceremonyUrl` as an event `ceremony`), then `{ peerId, address }` |
| `join.start` | `{ label }` | `{ joinAddress, peerId }` |
| `join.wait` | | blocks until the exchange completes; `{ introducer: { peerId, label }, pinned: n }` or an error naming the failed check |
| `add.start` | `{ address }` | `{ peerId, label, fingerprint }` of the newcomer, after the tunnel and its self-description arrive; nothing is signed yet |
| `add.confirm` | `{ peerId }` | `{ ceremonyUrl }`; then event `added { peer }` when done, or error |
| `add.cancel` | `{ peerId }` | `{}`; closes the join tunnel |

`init.wait`, `join.wait` and `add.confirm` are long-running; the client may set a
deadline and `cancel`.

### Messages

| op | request | result |
|---|---|---|
| `msg.send` | `{ to, topic, payload, encoding }` | `{ outcome: "delivered" \| "queued" \| "rejected", to, label, queueDepth?, reason? }` |
| `msg.subscribe` | `{ topic?, from?: [peerId…] }` | `{}`; then one `mail` event per envelope on this connection |
| `msg.ack` | `{ id }` | `{}` |
| `msg.queue` | `{ peer? }` | `{ outbound: [...], inbound: [...], quarantined: [...] }` |

A subscription lives as long as its connection. Each subscriber connection has one
in-flight envelope at a time; the next is sent after `msg.ack`.

### Streams

| op | request | result |
|---|---|---|
| `pty.open` | `{ peer, argv?, cwd?, env?, cols, rows }` | `{ streamId }`; attach within 10 s |
| `exec.open` | `{ peer, argv, cwd?, env? }` | `{ streamId }`; attach within 10 s |
| `stream.close` | `{ streamId }` | `{}` |

Resize and stdin-eof travel as control frames on the attach connection, not as ops.

### Events

| event | payload |
|---|---|
| `peer` | a `PeerView`, on any change: state, path, label, scopes, revoked, queue depth |
| `mail` | `{ envelope }` to a subscribed connection |
| `stream.closed` | `{ streamId, reason, exitCode?, signal? }` |
| `ceremony` | `{ ceremonyUrl }` during `init.wait` for the second ceremony |
| `added` | `{ peer }` after `add.confirm` completes |
| `trust-root-mismatch` | `{ peer }` when a peer's `sync` shows it holds a different passkey public key |

## Errors

`error` is a stable token, not prose: `unknown-peer`, `ambiguous-peer`, `revoked-peer`,
`not-enrolled`, `already-enrolled`, `scope`, `limit`, `params`, `offline`, `spawn`,
`attestation:wrong-passkey`, `attestation:bad-peer-id`, `attestation:bad-assertion`,
`ceremony-timeout`, `ceremony-state`, `queue-full`, `storage-failure`, `busy` (an
enrolment already in progress), `internal`. A human-readable `detail` may accompany it.
Clients switch on `error`, never on `detail`.

## What is not on the socket

- Anything that reads or writes `$BEAM_DIR` files directly. Every client goes through
  the daemon; there is no "fall back to editing `peers.json`" path.
- Presence probes. `state` is what the daemon knows from its own dials.
- Authentication. The socket's file mode is the boundary, as with tmux and Docker.
