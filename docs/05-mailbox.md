# 05 · Durable mailbox

A message is an opaque payload addressed to a peer, kept on the sender's disk until the
recipient's daemon has stored it, and on the recipient's disk until an application has
taken it. beam never parses `payload`; `topic` lets a receiver subscribe to what concerns
it.

## Envelope

```json
{ "id": "uuid", "from": "<peerId>", "to": "<peerId>", "seq": 42, "topic": "orchestra",
  "payload": "…", "encoding": "utf8" | "base64", "createdAt": 1758570000000 }
```

Payload ≤ 256 KiB decoded; serialized envelope ≤ 960 KiB, so that one fits a 1 MiB control
line ([06](06-control-socket.md)) with what wraps it. An envelope is stored as it arrived
and never goes out larger: no line beam writes escapes markup (`<`, `>`, `&`) or U+2028
and U+2029 in its JSON. `topic` 0–128 scalar values with
the label character rules. `seq` is a positive safe integer. A `base64` payload is unpadded
base64url, like every binary field in beam.

## Store

One SQLite database, `$BEAM_DIR/state.db`, WAL mode, `synchronous=FULL`. Tables:
`outbound(peer, seq, envelope, created_at)`, `inbound(peer, seq, id, topic, envelope,
received_at, inflight_to, deferred_to, deferred)`, `seen(peer, high_seq)`, `send_seq(peer,
next_seq)`, `quarantine(peer, seq, envelope, reason)`. `inbound` keeps the envelope's `id`
and `topic` for acks and subscriber filters, and for a deferred envelope why, and the
newest subscription when it was deferred. `inflight_to` and `deferred_to` name subscriptions of the
running daemon, so a daemon start clears them. Every state change below is one
transaction. There are no per-envelope files, no link tricks and no separate counter
files; durability is SQLite's.

Bounds per peer per direction: 10,000 envelopes, 64 MiB. A sender's outbound bound for a
recipient counts its quarantine for that recipient too.

## Sequence numbers

`seq` is per (sender, recipient), strictly increasing, allocated in the same transaction
that inserts the outbound row. The receiver keeps `seen.high_seq` per sender; an
envelope with `seq ≤ high_seq` is answered `accepted: false, reason: "duplicate"` and
the sender deletes it. Contiguity is not required.

A peer's `send_seq` row is created at 1 in the transaction that pins the peer, so one that
is missing is an error (`storage-failure` on send), never a restart at 1. `send_seq` and
`seen` belong to the keys, not the fleet: `beam fleet reset` keeps them, and a peer
pinned again continues where it was.

## Transactions

| Event | Transaction |
|---|---|
| `msg.send` | insert `outbound` with next seq; bump `send_seq` |
| receive on `msg` | if `seq ≤ high_seq`: no write, ack duplicate. Else insert `inbound`, set `high_seq = seq`, commit, **then** ack accepted |
| ack accepted arrives | delete `outbound` row |
| ack refused, permanent reason | move row to `quarantine` |
| subscriber takes envelope | set `inflight_to = <connection>` |
| `msg.ack` from subscriber | delete `inbound` row |
| `msg.defer` from subscriber | clear `inflight_to`; set `deferred_to` and `deferred` |
| subscriber connection lost | clear `inflight_to` for its rows |

Because `high_seq` and the `inbound` insert commit together, a crash cannot leave a
sender's message acknowledged but unstored, and a retry of an unacked message is
recognised as a duplicate only if it was actually stored.

## Delivery

One flusher per recipient, on the tunnel this machine dialed to it. Triggers: the peer
becoming `connected`, a new `outbound` row while connected, and a 2 s retry for an
unacked head while the tunnel lives. Strictly sequential: head, ack, delete, next. A
recipient that refuses the `msg` stream itself (its grant for this machine is `none`)
is asked again on new mail, or after 5 min, not every 2 s; the queue stays.

Permanent refusals (`payload-too-large`, `invalid-envelope`) quarantine the envelope so
it does not block the queue; `queue-full`, `storage-failure` and unknown reasons retry.

## Outcomes of `msg.send`

| Outcome | Meaning | Caller |
|---|---|---|
| `delivered` | recipient daemon stored it and acked | done |
| `stored` | on this machine's disk; delivery pending (peer offline, its grant refusing mail, or no ack within 10 s) | **success**; do not resend |
| `rejected` | nothing stored: `unknown-peer`, `revoked-peer` (revoked here, or refusing this machine as revoked), `invalid-topic`, `payload-too-large`, `queue-full`, `storage-failure`; or refused for good by the recipient within the 10 s (`payload-too-large`, `invalid-envelope`), and kept only in quarantine | failure |

`stored` carries `pendingReason: "offline" | "grant" | "no-ack"`. Every surface reports it as:

```
stored for workbox; delivery pending (workbox is offline). beam will deliver it when
workbox connects. Do not send it again.
```

for `grant`, as soon as the recipient refuses the stream:

```
stored for workbox; delivery pending (workbox's grant refuses mail from this machine).
beam will deliver it once workbox allows it. Do not send it again.
```

and, for `no-ack`:

```
stored for workbox; delivery pending (workbox has not acknowledged it). beam will keep
delivering it until workbox does. Do not send it again.
```

## Subscribers

Applications subscribe over the control socket with an optional `topic` and an optional
list of senders, `from`, applied by the daemon. Each subscriber connection has one
in-flight envelope at a time and acks it by its `id`, passed as `envelopeId`, after doing
something durable with it. An envelope no live subscriber matches stays in `inbound`. A
subscriber that never acks stalls only itself; one that disconnects, or leaves its
delivery unread for 10 s and is disconnected, releases its in-flight envelope for
redelivery. `msg.defer { envelopeId, reason }` releases an envelope without acking and
records `reason` for `msg.queue` and the desktop's refused list; it is offered again only
to a subscription made after the defer, such as the next subscribe on the same
connection. A delivery goes out only while its subscription is live and still holds the
envelope; a replacing subscribe, an ack or a defer waits out one under way, so nothing
reaches a connection after the reply that settled or replaced it.

`delivered` means the other daemon has it. Whether an application has acted on it is
that application's business, and beam does not claim exactly-once effects.
