# 12 · Decisions and open questions

Code cites decisions by number. Each entry is the decision and the reason; the mechanics
live in the document named.

## Register

| # | Decision | Why | Where |
|---|---|---|---|
| D1 | beam is a single Go binary, daemon plus CLI. | tailcat is a Go library with no TypeScript surface; the allowlist must live with the tunnel; a TypeScript core with a Go sidecar would hold the peer table in two processes. | 01, 09 |
| D2 | Transport is tailcat. | Production WireGuard, NAT traversal and DERP relay with no control plane, so no coordination server can introduce a key. Replaces WebRTC's signalling, DTLS pinning and the unsolved symmetric-NAT case. | 03 |
| D3 | One key per machine: the WireGuard node key. `peerId` derives from it. | Every inter-peer message travels through a tunnel that authenticates the sender, so nothing beam sends needs its own signature. A second key would exist only to sign things that do not need signing. | 02 |
| D4 | One passkey per person is the sole root of trust. | Hardware-bound, presence-gated, cannot be copied off a stolen disk. A single root has no key set to slip an entry into. | 01, 02 |
| D5 | The only hosted component is a static ceremony page with no network access. | WebAuthn requires a real origin. Everything else the worker did is done by the passkey, the tunnel, or gossip. | 02, 09 |
| D6 | Revocations and directory updates are unsigned; acceptance is "arrived over a tunnel from a pinned, unrevoked peer". | Follows from D3. Anyone in the fleet may revoke anyone, so forwarding losing the original issuer changes nothing. | 02 |
| D7 | A stream is one TCP connection through the tunnel; there is no multiplexer. | netstack gives real TCP; a muxer would re-implement flow control TCP already does. | 03, 04 |
| D8 | Streams carry a five-byte-header frame format after a one-line JSON open. | `pty` and `exec` need in-band control and a typed end; `msg` needs acks. Fifty lines, no ids, no windows. | 04 |
| D9 | One service port, `7000`, for all kinds. | Kind is in the header; per-kind listeners spread accept logic for nothing. | 03 |
| D10 | The fleet directory is gossiped on every `sync`; there is no hosted copy. | No server in the trust path; a fleet is small; every machine hears about a newcomer within one round of reconnects. | 02 |
| D11 | Revocation is permanent for a `peerId`. | Every holder re-sends it forever; an un-revoke would be undone by the next peer. A machine comes back with a new key. | 02 |
| D12 | The daemon outlives its clients; the desktop stops it only on explicit request. | A listener that dies with the UI is not a listener. | 06 |
| D13 | Byte streams cross the control socket on separate attach connections. | Base64 in the control channel is head-of-line blocking. | 06 |
| D14 | No client edits `$BEAM_DIR` files; the socket spawns the daemon if needed. | Two writers of `peers.json` is how the old code got `reload-peers`. | 06, 07 |
| D15 | Tailscale's public tailcat DERP map is the default; an own relay is a flag. | Zero infrastructure to start; the escape hatch is a VM with `derper`, not a rewrite. | 03 |
| D16 | A new peer's grant is all three stream kinds. | One human, one fleet, one tap per machine; narrowing is `peer grant` on the protected machine. | 02, 04 |
| D17 | `init` runs two ceremonies: register, then attest. | Keeps the page trivial and keeps registration and attestation separately verifiable. One extra tap, once per fleet. | 02 |
| D18 | No relay beyond DERP; no TURN. | DERP already is the relay of last resort. | 01 |
| D19 | `join` uses a single-use address with its own PSK and an open allowlist; the permanent address is exchanged inside. | The permanent server never runs allow-any, and the join capability expires. | 02, 03 |
| D20 | Revocation rebuilds the tailcat `Server` until upstream provides removal; admission is also checked at the beam layer. | `RemoveAllowedClient` does not exist in v0.7.0. Belt and braces cost a map lookup. | 03 |
| D21 | Two tunnels per pair, one per direction. | A tailcat `Server` cannot dial its clients; a stream uses the tunnel its opener dials. Simple, symmetric, cheap at fleet size. | 03 |
| D22 | Rotating the passkey means re-joining every machine. | Accepting a new root from a peer would let one stolen machine replace the owner. | 02 |
| D23 | Version pinned; each tailcat bump reviewed against `03-transport.md`. | tailcat is v0.x with declared instability. | 03, 09 |

## Open questions for review

Ordered by how much a wrong answer would cost. Each names the decision it would change.

1. **Is one key enough?** (D3, D6) The claim: because every message arrives through an
   authenticated tunnel, nothing needs an application-layer signature. Attack this.
   Cases to consider: a revocation forwarded by B that A never issued (accepted, by
   design — is that right?); a directory entry whose `address` is wrong (the node key in
   it must match the attested key, so a wrong address fails to connect — denial only);
   anything that must survive being stored and re-checked later without the tunnel
   present. The attestation is the one thing checked offline, and it is passkey-signed.
2. **Should the disco key be in the attestation?** (02) It is included so a peer cannot
   be steered to a different path-discovery identity. Does that buy anything given the
   WireGuard handshake pins the node key regardless? If not, removing it simplifies
   `key.json` handling and address rotation.
3. **The join window.** (D19) For up to ten minutes a `Server` with the real node key
   accepts any client that holds the join address. The address carries a fresh PSK, so
   only the holder can connect, and the newcomer receives nothing until an attestation
   arrives. Is "first handshake wins, then lock" sufficient, or should the newcomer show
   the connector's fingerprint and require a local confirm too?
4. **Trusting the introducer for the passkey public key.** (01) The bounded argument:
   a lie makes the newcomer refuse the real fleet, never accept a fake one alongside.
   Is there a construction where a malicious introducer gets a newcomer that works with
   *both*? The newcomer verifies every directory attestation against the received key,
   so real machines fail; but could the introducer include attestations under its own
   fake root for machines whose node keys it does not control? It cannot connect to
   them, so they would sit as dead pins — check that dead pins are harmless.
5. **Server rebuild on revoke.** (D20) Peers see a drop of a few seconds. Acceptable
   for the interim? Is there a cheaper interim, e.g. keep the `Server` and rely solely on
   the beam-layer admission check until upstream lands, accepting that the revoked
   peer's WireGuard session stays up but every stream is refused?
6. **No presence.** (03) Without a control plane the daemon dials on a backoff forever.
   For a laptop on battery with three offline peers that is a DERP round trip each every
   60 s. Fine? Should there be a "give up after N hours until user action" state?
7. **Unlimited gossip trust in `address` updates.** (D10) Any pinned peer may update
   another peer's address. The node key in the address must match the pin, so the worst
   case is a peer being pointed at an address that does not answer. Should an address
   update require that the *subject* peer has been seen at it, or is denial-of-service
   by a fleet member out of scope (they could revoke instead)?
8. **Passkey rotation cost.** (D22) Re-join every machine. With ten machines that is
   ten address pastes and ten taps. Is there a safe shortcut? (The obvious ones all let
   a peer replace the root.)
9. **Ceremony ergonomics.** (02) Loopback redirect from a real origin: Safari and the
   fragment, Chrome's private-network-access rules for top-level navigations (should be
   exempt), Firefox on Linux without a default browser, WSL. Does the page need a manual
   "copy this and paste into the terminal" fallback for environments where the redirect
   cannot land?
10. **Default grant of all three kinds.** (D16) Every added machine may open a shell on
    every other. One human, so this is the SSH-key-everywhere model. Should `join`
    offer `--grant msg` for a machine that should only report?
11. **`peerId` is 64 bits.** A second-preimage against a given id is infeasible; a
    collision among an attacker's own keys is ~2^32 work and buys nothing because the
    accepting side would refuse a pin whose key differs. Keep 16 hex characters for
    display, or widen to 32?
12. **Windows.** Named pipe for the socket, ConPTY for `pty`, no Windows CI. Is Windows
    a first-release target or a later one? The decision changes the `stream` and
    `control` packages' first shape.
13. **Two ceremonies at `init`.** (D17) Could be one with a page that does `create` then
    `get`. Worth the page complexity to save a tap that happens once per fleet?
14. **Attach timeout.** (D13) Ten seconds to attach after `pty.open`. Electron under load
    on first launch — enough?
15. **The fingerprint prompt.** `add` shows the newcomer's `peerId` in groups of four and
    the newcomer prints the same. Is a 16-hex-character comparison the right length for
    a human who will usually skip it, or should it be a word list (PGP-style) to make the
    optional check actually get done?
