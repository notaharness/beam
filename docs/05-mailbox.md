# 05 · Durable mailbox

A message is an opaque payload addressed to a peer, delivered when a tunnel exists,
and kept on disk on both sides until the application that wanted it has taken it. beam
never parses `payload`; `topic` exists so a receiver can subscribe to what concerns it.

Flush is triggered by the daemon's `connected` transition and nothing else; there is no
presence oracle to consult.

## Envelope

```json
{
  "id": "uuid",
  "from": "<sender peerId>",
  "to": "<recipient peerId>",
  "seq": 42,
  "topic": "orchestra",
  "payload": "…",
  "encoding": "utf8" | "base64",
  "createdAt": 1758570000000
}
```

Payload cap 256 KiB on the decoded bytes, and the serialised envelope must fit one
frame (1 MiB). Both are checked on both sides: `send` refuses what could never be sent;
a receiver refuses what is over the cap rather than storing past it.

## Storage

```
$BEAM_DIR/mailbox/
  seq.json             next seq per recipient
  out/<peerId>/        one file per undelivered envelope, named by zero-padded seq
  in/<peerId>/         one file per received envelope not yet taken by a subscriber
  seen/<peerId>.json   highest accepted seq from that sender
  corrupt/             quarantined files, reported as lost
```

Queue files are written to a temp path then **linked** into place: an exclusive create
that fails rather than replacing, so a same-seq collision is loud. Bounds per peer, each
direction: 10,000 envelopes and 64 MiB.

## Sequence numbers

`seq` is per **(sender, recipient) pair**, strictly increasing. The receiver keeps the
highest accepted `seq` per sender, accepts anything above it, and re-acks without
re-delivering anything at or below — the duplicate a crash between delivery and ack
produces. The receiver does **not** require contiguity: order comes from the sender
draining sequentially over TCP, and a receiver that wedged on a hole it can never fill
would be worse than one that notices nothing.

`SeqCounter.next()` reconciles against the highest seq on that peer's own outbound disk,
so a lost counter with a non-empty backlog cannot reissue a number. A lost counter with
an *empty* backlog restarts at 1 and the next real message is judged a duplicate by the
receiver. This hole is known and not closed; closing it needs the receiver's high-water
mark, which nothing asks for.

## Two acknowledgements

| Ack | Sent by | Means | Effect |
|---|---|---|---|
| wire ack | receiving daemon, on the `msg` stream | the envelope is on the receiver's disk (`in/`) | sender unlinks its copy and reports `delivered` |
| subscriber ack | an application, on the control socket | it has done something durable with the envelope | receiver unlinks its copy |

`delivered` therefore means "on the other machine's disk", not "an application saw it".
Anything still in `in/` at daemon start is redelivered to subscribers. There is no
ack-on-receipt mode for subscribers; a consumer that merely observes acks immediately
and says so by doing it.

## Delivery

Strictly sequential per peer: send the head of `out/<peerId>/`, wait for its ack, unlink,
next. Triggers: the peer becoming `connected`, daemon start (for peers already
connected), and a retry every 2 s for an envelope that is not acked while the tunnel
stays up. There are no timers for offline peers; there is nothing to try.

## Send outcomes

| Outcome | Meaning | Caller |
|---|---|---|
| `delivered` | the recipient acked | done |
| `queued` | no tunnel, or no ack before the timeout; the envelope is on disk | **success**; do not resend |
| `rejected` | nothing stored | failure; may retry if the reason is transient |

`rejected` names its cause: `unknown-peer`, `revoked-peer`, `invalid-topic`,
`payload-too-large`, `queue-full`, `storage-failure`. A seq is claimed only once the
file is on disk, so a failed write leaves no gap.

The CLI and every consumer must report `queued` in these words or their equivalent,
because a human or agent that does not understand it will send again:

```
queued for workbox — that machine is not connected right now. beam will deliver this
message when it comes online. Do not send it again.
```

## Quarantine

A queue file that cannot be read, an envelope that cannot be encoded, or an envelope the
receiver refuses with a reason that can never change (`payload-too-large`) is moved to
`corrupt/`, logged, surfaced through `beam msg queue` and the control socket, and no
longer blocks the queue behind it. Reasons that can clear (`queue-full`,
`seen-unreadable`) keep their place and retry. An unknown reason is treated as
transient. A `seq.json` or `seen/*.json` that cannot be read is refused outright, never
reset: a restarted sequence makes the receiver ack real messages as duplicates.

## Topic and label at the boundary

`topic` is 0–128 characters, no `/`, `\`, `{`, `}` or control characters; the empty
topic is valid and means "no topic". `peerId` is the only field that reaches a path and
is checked as 16 lowercase hex.

## Subscribers

Applications subscribe over the control socket ([06-control-socket](06-control-socket.md))
with optional `topic` and `from` filters applied daemon-side; an envelope no subscriber
wants stays in `in/` for one that does. Each subscriber has its own in-flight slot: one
envelope handed and not yet acked. A subscriber that never acks stalls only itself.
