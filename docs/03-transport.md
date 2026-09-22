# 03 · Transport: tailcat

beam links `github.com/tailscale/tailcat` as a Go library: wireguard-go, magicsock, gVisor
netstack and the DERP client, with no control plane. API names are v0.7.0; the version is
pinned and each bump is reviewed against this document.

## The constraint that shapes the topology

Every tailcat `Server` and every tailcat `Client` is its own networking stack with its
own WireGuard identity, and each registers that identity with DERP. DERP delivers
packets for a key to one connection, the most recent writer. Two stacks on one machine
with the same key therefore steal each other's inbound traffic. **One key, one stack.**

So a machine runs:

- **One `Server`** with the machine's node key from `key.json`. Its `TailcatAddr()` is
  the address in the machine's signed entry. It accepts any client that holds the
  address (allow-any; `AllowedClients` is never set, since setting it once turns the
  server restrictive).
- **One `Client` per peer it dials**, each with a **fresh random node key for this
  process lifetime** (`key.NewNode()`), dialling the peer's address. Distinct keys,
  distinct stacks, no DERP collision. The peer sees an unknown key, which is why
  admission below exists.

Each pair therefore has two tunnels, one per direction, and every stream travels on the
tunnel its opener dialed. Streams are directional: the acceptor never opens anything on
a tunnel it did not dial. This is what makes ownership unambiguous: `A→B` mail is
flushed on A's client tunnel to B by one flusher; `B→A` on B's.

`tailcat.Server` lacks an outbound dial. If upstream adds one, a machine collapses to a
single stack and the possession proof below becomes unnecessary; the signed entry and
admission stay as they are.

## Addresses

A tailcat address (`tc…`, unpadded base64url CBOR, variable length) carries the server's
node key, disco key, pre-shared key and DERP region details including relay hostnames.
Holding it is necessary to complete a handshake; the PSK is mixed into Noise, so a relay
that observes public keys cannot. It contains no endpoint IP of the machine.

In beam an address is not a secret: it lives encrypted in the directory and in plaintext
on every member. A revoked machine holds every address and can complete handshakes; it
is closed at admission. That is accepted, with the transport-level resource exposure it
implies ([01](01-model.md), threat model).

Relay coordinates are inside the signed entry. A machine that moves to another DERP map
re-runs `beam join` (one tap) and the new entry supersedes the old.

## Admission

A tunnel arrives at the `Server` from an unknown client key `C`. Nothing is trusted until
the dialer opens a `hello` stream ([04](04-streams.md)) and the acceptor checks:

1. **Membership.** The frame carries the dialer's signed entry `E`. Verify it
   ([02](02-identity.md)); it names the dialer's real node key `S`.
2. **Possession.** `C` is ephemeral and not in any entry, so the dialer proves it holds
   `S`'s private key without a signature: both sides compute `k = X25519(s, R) = X25519(r,
   S)` where `R` is the acceptor's node key (the dialer knows it from the address it
   dialed). The frame carries `mac = HMAC-SHA256(k, "beam-hello:v1" ‖ C ‖ S ‖ R ‖ nonce)`
   with the dialer's 32-byte `nonce`; the acceptor recomputes it. `C` is taken from the
   tunnel, not the frame, so a valid `(E, mac)` cannot be replayed from a different
   tunnel.
3. **Not revoked.** `E.peerId` has no stored revocation, checked after any revocations
   already received on this or other tunnels have been applied.

Pass: bind `C → E.peerId` for this tunnel, pin `E` if new (and schedule a dial back),
answer `ok`. Fail: answer `refused` with a reason and close. Any other first stream from
an unbound `C`, or any stream after a failed hello, is closed unread. Budgets: 16
concurrent unbound tunnels in hello, 5 s each; beyond that the oldest is closed. tailcat
keeps its own per-peer state for a client that handshook; beam cannot evict it and does
not claim to.

The acceptor learns the peer behind a TCP connection from `Server.PeerEnv(local,
remote)` (`TAILCAT_PEER_KEY=nodekey:…`), failing closed when absent, until upstream
exposes a `PeerKey` lookup.

## Streams are TCP

```go
ln, _  := server.Listen(ctx, "tcp", ":7000")
conn, _ := client.DialTCPPort(ctx, 7000)
```

One TCP connection per stream through netstack; ordering, flow control and half-close
are TCP's. No multiplexer. One port, kind in the header.

## Lifecycle

- **Up.** For each member, create a `Client`, `DialTCPPort(7000)` with a 20 s deadline,
  open `hello`, then `sync`. `connected` means hello succeeded on *our* dialed tunnel;
  the peer's own dial to us is independent and reported separately as `inbound`.
- **Liveness.** A `ping` control frame on the `sync` stream every 15 s; no reply within
  30 s closes the tunnel. WireGuard keepalives and `Server.Status()` are advisory.
- **Retry.** Exponential backoff 2 s → 5 min with jitter, forever while the daemon runs;
  reset on inbound contact from that peer, on a learned entry for it, and on
  `msg.send` to it. There is no give-up state: queued mail must eventually flow.
- **Path.** `Server.Status()` gives `CurAddr` or `Relay` for inbound peers; the `Client`
  has no equivalent, so `path` is reported for the inbound tunnel or as `unknown`.

| State | Meaning |
|---|---|
| `connected` | hello succeeded on the tunnel this machine dialed |
| `offline` | last dial failed or liveness lapsed; retrying |
| `revoked` | refused at admission; never dialed |

## DERP

Default map `https://tailcat.dev/derpmap.json` (Tailscale's tailcat fleet: rate-limited,
metadata-logged, no SLA). Cached in `derpmap.json`. `beam daemon --derp-map URL` for a
self-hosted `derper`. Tests use an in-process relay ([10](10-testing.md)).

## Footprint (measured, one Server and one Client, Go 1.27.1, linux/amd64)

Binary 15–16.4 MB stripped across five targets; cold build 21.5 s; warm 0.14 s; module
cache 479 MB; `CGO_ENABLED=0`; ~26 MB RSS. Per-peer client stacks add to that; the
milestone-1 spike measures a five-peer fleet.
