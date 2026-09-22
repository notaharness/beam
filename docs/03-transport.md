# 03 · Transport: tailcat

beam links `github.com/tailscale/tailcat` as a Go library. tailcat is Tailscale's data
plane (wireguard-go, magicsock, gVisor netstack, DERP client) with no control plane:
point-to-point WireGuard tunnels, NAT traversal via STUN and disco, DERP as the
rendezvous side channel and the relay of last resort. Connection metadata — who may
connect to whom — is beam's problem, and [02-identity](02-identity.md) answers it.

API names below are tailcat v0.7.0. The library declares no stability promise; the
version is pinned and each bump is reviewed against this document.

## Two roles per machine

tailcat's `Server` accepts; its `Client` dials. A `Server` cannot dial its clients. So
every machine runs:

- **One `Server`**, bound to this machine's node key, disco key and pre-shared key from
  `key.json`, with `AllowedClients` set to the node keys of every pinned, unrevoked
  peer. Its `TailcatAddr()` is this machine's **permanent address**, the string that
  travels in the fleet directory.
- **One `Client` per pinned peer**, dialling that peer's permanent address with this
  machine's node key. The tunnel is brought up lazily by the first `Dial` and shared by
  every later one.

A pair of machines therefore has two tunnels between them, one in each direction. A
stream is opened over the tunnel whose `Client` end the opener owns. This is simple and
symmetric, and costs one extra WireGuard session per pair; with a fleet of single-digit
size that is nothing.

## Addresses

A tailcat address (`tc…`, ~150 characters) encodes `ServerPublic`, `ServerDiscoPublic`,
`PresharedKey` and `RegionID`. It is a **capability**: holding it is necessary to
complete a handshake with that server, and with the PSK in it a DERP operator who sees
the public keys still cannot. It is not sufficient: the server also checks the client's
node key against `AllowedClients`.

beam uses two kinds:

| | Pre-shared key | Allowlist | Lifetime |
|---|---|---|---|
| Join address | fresh random, single use | any client, first handshake wins | 10 minutes or one use |
| Permanent address | from `key.json`, stable | pinned peers only | the life of the machine |

The permanent address lives in every peer's `peers.json`. Rotating a machine's PSK
(`beam key rotate-psk`, not in the first release) changes its permanent address; the
new address travels by gossip and the old one stops working.

## Allowlist

`Server.AllowedClients` is the second half of mutual authentication. It is the set of
`NodePublic` from `peers.json` where `Revoked == false`. It is rebuilt from the peer
table on every change to the table.

tailcat v0.7.0 has `AddAllowedClient` but no `RemoveAllowedClient` (issue #124) and no
per-connection admission callback (issue #119, `AllowClient func(key) bool`, maintainer
in favour). Until one lands:

- **Revocation** rebuilds the `Server`: close it, construct a new one with the reduced
  allowlist, listen again. Peers see their tunnel drop and reconnect within seconds;
  their `Client` ends retry automatically. This is acceptable for an operation that
  happens rarely and whose whole point is to drop a peer.
- Independently, every accepted TCP connection is checked at the beam layer against the
  peer table before its open header is read (see [04-streams](04-streams.md)). A revoked
  peer whose tunnel is somehow still up gets every stream refused. This check stays even
  after upstream provides removal; it costs a map lookup.

beam will send the `AllowClient` PR upstream; it is ~30 lines and removes the rebuild.

## Identifying the peer behind a connection

An accepted `net.Conn` from `Server.Listen` carries the peer's tunnel address, not its
key. v0.7.0 exposes the mapping only via `Server.PeerEnv(local, remote)`, which returns
`TAILCAT_PEER_KEY=nodekey:…` among other strings. beam parses that until a
`Server.PeerKey(netip.Addr) (key.NodePublic, bool)` exists upstream (to be filed). The
lookup result is matched against the peer table; a key not in the table closes the
connection before a byte is read.

## Streams are TCP

gVisor netstack gives real TCP through the tunnel:

```go
ln, _ := server.Listen(ctx, "tcp", ":7000")     // beam's one service port
conn, _ := client.Dial(ctx, "tcp", "[<server tunnel ip>]:7000")
```

Each beam stream is one TCP connection. Ordering, flow control, backpressure and
half-close are TCP's. There is no application multiplexer, no stream id space, no frame
window.

One port, `7000`, for every stream kind; the first line of the connection says which
(see [04-streams](04-streams.md)). Per-kind ports would spread the accept logic across
listeners for no gain.

## Connection lifecycle

- **Up.** The daemon starts the `Server`, then for each pinned, unrevoked peer creates a
  `Client` and dials `:7000` for a `sync` stream. Tunnel establishment is triggered by
  that dial. Success means: exchange `sync`, mark the peer `connected`, flush its
  mailbox queue.
- **Retry.** A failed dial retries with exponential backoff from 2 s to a cap of 60 s,
  forever, while the daemon runs. There is no presence oracle (no control plane), so
  "is this peer online" is answered only by trying. With DERP as rendezvous a failed
  attempt to an offline peer costs one DERP round trip and nothing else. The backoff
  resets when a peer connects inbound to us — its arrival is proof it is up, so we dial
  back immediately to bring up our own direction.
- **Down.** A tunnel that goes silent is detected by WireGuard keepalives and
  `Server.Status()`; a TCP stream through a dead tunnel gets a reset. The daemon marks
  the peer `offline` and re-enters retry. Mailbox flush stops until the next `connected`.
- **Path.** `Server.Status()` reports per peer `CurAddr` (direct endpoint) or `Relay`
  (DERP region). The daemon exposes this on the control socket as `path: "direct"` or
  `path: "relay:fra"` so the UI can show it.

Peer states, as the control socket and CLI report them:

| State | Meaning |
|---|---|
| `connected` | at least one tunnel up and a `sync` exchanged |
| `offline` | last dial failed; retrying |
| `revoked` | in the peer table, never dialed, never allowed |

There is no `unknown` and no `reachable`: without a control plane the daemon knows only
what it has tried.

## DERP

- **Map.** `Server.DERPMapURL` / `Client.DERPMapURL` default to
  `https://tailcat.dev/derpmap.json`, Tailscale's dedicated tailcat relay fleet (four
  regions at the time of writing). Tailscale states it is rate-limited, logs metadata,
  carries no SLA and may be withdrawn. The map is cached in `$BEAM_DIR/derpmap.json` and
  used stale rather than failing when the fetch fails.
- **Own relay.** `beam daemon --derp-map <url>` and a `derpMapURL` line in `key.json`
  point at a map we host. Running `tailscale.com/cmd/derper` on a VM with a public IP is
  the escape hatch if the public fleet becomes a problem; the address format carries the
  region so peers follow.
- **Dev.** Tests use an in-process DERP + STUN on loopback; see [10-testing](10-testing.md).

## What tailcat does not do for us

- Introduce keys. That is the attestation.
- Multiplex. That is TCP.
- Persist anything but `key.json`. Peers, revocations, mailbox are beam's.
- Interoperate with a running tailscaled. It does not, by upstream decision, and beam
  does not try.

## Footprint (measured, Go 1.27.1, linux/amd64)

| | |
|---|---|
| Binary, `-s -w` + tailcat's `build-tags.txt` | 15–16.4 MB across the five targets |
| Cold build, module cache warm | 21.5 s |
| Warm rebuild | 0.14 s |
| Module cache | 479 MB |
| cgo | not required; `CGO_ENABLED=0` for every target |
| RSS with one tunnel up | ~26 MB |
