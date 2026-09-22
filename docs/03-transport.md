# 03 · Transport: tailcat

beam links `github.com/tailscale/tailcat` as a Go library. tailcat is Tailscale's data
plane (wireguard-go, magicsock, gVisor netstack, DERP client) with no control plane:
point-to-point WireGuard tunnels, NAT traversal via STUN and disco, DERP as the
rendezvous side channel and the relay of last resort. Who may connect to whom is beam's
problem, answered by [02-identity](02-identity.md) and enforced in "Admission" below.

API names are tailcat v0.7.0. The library declares no stability promise; the version is
pinned and each bump is reviewed against this document.

## Two roles per machine

tailcat's `Server` accepts; its `Client` dials; a `Server` cannot dial its clients. So
every machine runs:

- **One `Server`**, bound to this machine's node key, disco key and pre-shared key from
  `key.json`, accepting any client that holds the address. Its `TailcatAddr()` is the
  **address** that appears in this machine's signed membership entry.
- **One `Client` per pinned peer**, dialling that peer's address with this machine's
  node key. The tunnel is brought up lazily by the first `Dial` and shared thereafter.

A pair of machines therefore has two tunnels, one per direction; a stream is opened over
the tunnel whose `Client` end the opener owns. One extra WireGuard session per pair,
which at fleet size is nothing.

## Addresses

A tailcat address (`tc…`, ~150 characters) encodes `ServerPublic`, `ServerDiscoPublic`,
`PresharedKey` and `RegionID`. Holding it is necessary to complete a handshake with that
server: the PSK is mixed into the Noise handshake, so a DERP operator who observes the
public keys still cannot. It contains no IP; the server is found through DERP.

An address is not a secret in beam's model. It lives encrypted in the directory and in
plaintext on every fleet machine, so a revoked machine holds every address. What the
holder of an address gets is a completed WireGuard handshake and a TCP connection to
port 7000 that beam closes at admission. That is the trade for admitting on first
contact, and it is bounded to nuisance.

## Admission

tailcat's `AllowedClients` is **not used**; the `Server` accepts any client. beam decides
who is a peer at the application layer, per connection:

1. On accept, resolve the connection's remote tunnel address to a node key. v0.7.0
   exposes this only through `Server.PeerEnv(local, remote)`, which returns
   `TAILCAT_PEER_KEY=nodekey:…` among other strings; beam parses that until a
   `Server.PeerKey(netip.Addr)` exists upstream (to be filed).
2. Look the key up in the peer table.
   - **Pinned and not revoked:** proceed to read the open header.
   - **Revoked:** close. Nothing is read.
   - **Unknown:** read the open header. It must be `kind: "sync"` and its first frame
     must carry a membership entry whose `nodePublic` equals the key from step 1 and
     which verifies under `MPK` ([02-identity](02-identity.md)). On success, pin the peer,
     add it to the allow set for this process, and dial back. Anything else closes the
     connection. At most one unknown-key connection is processed at a time per source
     key, and unverified connections are dropped after 5 s.

This is what lets a machine that tapped once connect to a fleet nobody told about it.
It is also why revocation needs no cooperation from tailcat: a revoked key is refused
here and its existing tunnel is terminated.

## Streams are TCP

gVisor netstack gives real TCP through the tunnel:

```go
ln, _ := server.Listen(ctx, "tcp", ":7000")
conn, _ := client.Dial(ctx, "tcp", "[<server tunnel ip>]:7000")
```

Each beam stream is one TCP connection. Ordering, flow control, backpressure and
half-close are TCP's. There is no application multiplexer, no stream id space, no frame
window. One port for every stream kind; the first line of the connection says which
([04-streams](04-streams.md)).

## Connection lifecycle

- **Up.** The daemon starts the `Server`, then for each pinned, unrevoked peer creates a
  `Client` and dials `:7000` for a `sync` stream. Success: exchange `sync`, mark
  `connected`, flush that peer's mailbox queue.
- **Retry.** A failed dial retries with exponential backoff from 2 s to 60 s, forever,
  while the daemon runs. There is no presence oracle, so "is this peer online" is
  answered only by trying; via DERP a failed attempt costs one relay round trip. The
  backoff resets when the peer connects inbound to us, which proves it is up.
- **Down.** WireGuard keepalives and `Server.Status()` detect a dead tunnel; a TCP stream
  through it gets a reset. Mark `offline`, re-enter retry.
- **Path.** `Server.Status()` reports per peer `CurAddr` (direct) or `Relay` (DERP
  region), exposed on the control socket as `path: "direct"` or `path: "relay:fra"`.

| State | Meaning |
|---|---|
| `connected` | a tunnel is up and `sync` has been exchanged |
| `offline` | last dial failed; retrying |
| `revoked` | in the table, never dialed, refused at admission |

## DERP

- **Map.** Defaults to `https://tailcat.dev/derpmap.json`, Tailscale's dedicated tailcat
  relay fleet. Tailscale states it is rate-limited, logs metadata, carries no SLA and may
  be withdrawn. The map is cached in `$BEAM_DIR/derpmap.json` and used stale rather than
  failing when the fetch fails.
- **Own relay.** `beam daemon --derp-map <url>` points at a map we host; running
  `tailscale.com/cmd/derper` on a VM with a public IP is the escape hatch. The address
  carries the region so peers follow.
- **Dev.** Tests use an in-process DERP + STUN on loopback ([10-testing](10-testing.md)).

## What tailcat does not do for us

- Decide membership. That is the signed entry, checked at admission.
- Multiplex. That is TCP.
- Persist anything but `key.json`.
- Interoperate with a running tailscaled. It does not, by upstream decision.

## Footprint (measured, Go 1.27.1, linux/amd64)

| | |
|---|---|
| Binary, `-s -w` + tailcat's `build-tags.txt` | 15–16.4 MB across the five targets |
| Cold build, module cache warm | 21.5 s |
| Warm rebuild | 0.14 s |
| Module cache | 479 MB |
| cgo | not required; `CGO_ENABLED=0` for every target |
| RSS with one tunnel up | ~26 MB |
