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

Payload ≤ 256 KiB decoded; serialized envelope ≤ 1 MiB. `topic` 0–128 scalar values with
the label character rules. `seq` is a positive safe integer.

## Store

One SQLite database, `$BEAM_DIR/state.db`, WAL mode, `synchronous=FULL`. Tables:
`outbound(peer, seq, envelope, created_at)`, `inbound(peer, seq, envelope, received_at,
inflight_to)`, `seen(peer, high_seq)`, `send_seq(peer, next_seq)`, `quarantine(peer, seq,
envelope, reason)`. Every state change below is one transaction. There are no
per-envelope files, no link tricks and no separate counter files; durability is SQLite's.

Bounds per peer per direction: 10,000 envelopes, 64 MiB.

## Sequence numbers

`seq` is per (sender, recipient), strictly increasing, allocated in the same transaction
that inserts the outbound row. The receiver keeps `seen.high_seq` per sender; an
envelope with `seq ≤ high_seq` is answered `accepted: false, reason: "duplicate"` and
the sender deletes it. Contiguity is not required.

A `send_seq` row that is missing for a peer with any history is an error (`storage-failure`
on send), never a restart at 1.

## Transactions

| Event | Transaction |
|---|---|
| `msg.send` | insert `outbound` with next seq; bump `send_seq` |
| receive on `msg` | if `seq ≤ high_seq`: no write, ack duplicate. Else insert `inbound`, set `high_seq = seq`, commit, **then** ack accepted |
| ack accepted arrives | delete `outbound` row |
| ack refused, permanent reason | move row to `quarantine` |
| subscriber takes envelope | set `inflight_to = <connection>` |
| `msg.ack` from subscriber | delete `inbound` row |
| subscriber connection lost | clear `inflight_to` for its rows |

Because `high_seq` and the `inbound` insert commit together, a crash cannot leave a
sender's message acknowledged but unstored, and a retry of an unacked message is
recognised as a duplicate only if it was actually stored.

## Delivery

One flusher per recipient, on the tunnel this machine dialed to it. Triggers: the peer
becoming `connected`, a new `outbound` row while connected, and a 2 s retry for an
unacked head while the tunnel lives. Strictly sequential: head, ack, delete, next.

Permanent refusals (`payload-too-large`, `invalid-envelope`) quarantine the envelope so
it does not block the queue; `queue-full`, `storage-failure` and unknown reasons retry.

## Outcomes of `msg.send`

| Outcome | Meaning | Caller |
|---|---|---|
| `delivered` | recipient daemon stored it and acked | done |
| `stored` | on this machine's disk; delivery pending (peer offline, or no ack within 10 s) | **success**; do not resend |
| `rejected` | nothing stored: `unknown-peer`, `revoked-peer`, `invalid-topic`, `payload-too-large`, `queue-full`, `storage-failure` | failure |

`stored` carries `pending_reason: "offline" | "no-ack"`. Every surface reports it as:

```
stored for workbox; delivery pending (workbox is offline). beam will deliver it when
workbox connects. Do not send it again.
```

## Subscribers

Applications subscribe over the control socket with optional `topic` and `from`
filters applied by the daemon. Each subscriber connection has one in-flight envelope at
a time and acks by id after doing something durable with it. An envelope no live
subscriber matches stays in `inbound`. A subscriber that never acks stalls only itself;
one that disconnects releases its in-flight envelope for redelivery. `msg.defer { id,
reason }` releases an envelope without acking and records `reason` for `msg.queue` and
the desktop's refused list; it is redelivered on the next subscribe.

`delivered` means the other daemon has it. Whether an application has acted on it is
that application's business, and beam does not claim exactly-once effects.
