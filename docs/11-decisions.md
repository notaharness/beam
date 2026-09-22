# 11 · Decisions and open questions

Code cites decisions by number. Each entry is the decision and the reason; the mechanics
live in the document named.

## Register

| # | Decision | Why | Where |
|---|---|---|---|
| D1 | beam is a single Go binary, daemon plus CLI. | tailcat is a Go library with no TypeScript surface; the process that owns the tunnel must own admission. | 01, 09 |
| D2 | Transport is tailcat. | Production WireGuard, NAT traversal and DERP relay with no control plane, so no coordination server can introduce a key. | 03 |
| D3 | One key per machine: the WireGuard node key. `peerId` derives from it. | Everything a machine sends travels through a tunnel that authenticates it. A second per-machine key would sign things that do not need signing. | 02 |
| D4 | One passkey per person is the sole root; fleet keys are derived from it with the PRF extension. | Hardware-bound and presence-gated. PRF turns a signature-only credential into a source of secrets, so the root can sign (via `MSK`) and encrypt (via `K_dir`) without any of it living on a disk. | 02 |
| D5 | Only a tap mints or revokes membership. `MSK` exists in memory during a ceremony and nowhere else. | A compromised fleet machine must not be able to add or remove machines. | 01, 02 |
| D6 | Admission is at the beam layer on first contact; tailcat's allowlist is unused. | Lets a machine that tapped once connect to a fleet nobody told about it, with no polling and no push. Also makes revocation independent of tailcat's API. | 03 |
| D7 | The directory is an encrypted append-only log on an open-source worker, read at join and daemon start, appended at join and revoke, never polled. | A newcomer needs addresses from somewhere; the worker cannot read, forge or roll back what it stores; nothing depends on it at runtime. | 02, 09 |
| D8 | Revocations are passkey-signed and permanent. | Only the owner can revoke, so a stolen machine cannot revoke the fleet. Permanent because every holder re-sends it forever; a machine returns with a new key. | 02 |
| D9 | Entries and revocations are gossiped on `sync`. | Machines offline during a join or revoke catch up from peers; the fleet survives the worker losing data. | 02, 04 |
| D10 | A stream is one TCP connection through the tunnel; no multiplexer. | netstack gives real TCP; a muxer would re-implement flow control TCP already does. | 03, 04 |
| D11 | Streams carry a five-byte-header frame format after a one-line JSON open. | `pty` and `exec` need in-band control and a typed end; `msg` needs acks. | 04 |
| D12 | One service port, `7000`, for all kinds. | Kind is in the header. | 03 |
| D13 | The daemon outlives its clients; the desktop stops it only on explicit request. | A listener that dies with the UI is not a listener. | 06 |
| D14 | Byte streams cross the control socket on separate attach connections. | Base64 in the control channel is head-of-line blocking. | 06 |
| D15 | No client edits `$BEAM_DIR` files; the socket spawns the daemon if needed. | Two writers of `peers.json` is how a previous design grew a `reload-peers` op. | 06, 07 |
| D16 | Tailscale's public tailcat DERP map is the default; an own relay is a flag. | Zero infrastructure to start; the escape hatch is a VM, not a rewrite. | 03 |
| D17 | A new peer's grant is all three stream kinds. | One human, one fleet, one tap per machine; narrowing is `peer grant` on the protected machine. | 02, 04 |
| D18 | No relay beyond DERP; no TURN. | DERP already is the relay of last resort. | 01 |
| D19 | Two tunnels per pair, one per direction. | A tailcat `Server` cannot dial its clients. Simple, symmetric, cheap at fleet size. | 03 |
| D20 | Losing the passkey means a new fleet: `init` once, `join` everywhere. | Any path that migrates a fleet to a new root without a tap per machine lets a peer replace the owner. | 02 |
| D21 | Version pinned; each tailcat bump reviewed against `03-transport.md`. | tailcat is v0.x with declared instability. | 03, 09 |
| D22 | The worker never sees `K_auth`; it stores a derived verifier. Auth proves "a member", not which one. | Members already hold the plaintext; finer auth would protect nothing. | 09 |

## Open questions for review

Ordered by how much a wrong answer would cost. Each names the decision it would change.

1. **Deriving a signing key from PRF output.** (D4, D5) `MSK` is an Ed25519 key whose
   seed is HKDF of a PRF secret, reconstituted in daemon memory for one operation. The
   passkey never signs directly. Is anything lost versus a direct WebAuthn assertion
   over the entry? The gain is that verifiers need only Ed25519 and no WebAuthn library.
   The risk is the seconds `MSK` spends in a process's memory on a machine that may be
   compromised — an attacker who owns the machine at that moment can sign anything. Is
   that acceptable, or should signing happen in the page (which has the same exposure to
   a hostile page but not to a compromised daemon)?
2. **Allow-any at the WireGuard layer.** (D6) Anyone holding an address completes a
   handshake and reaches admission. Addresses are on every fleet machine and encrypted
   in the log, so the realistic holder is a revoked machine. Is nuisance-level exposure
   acceptable for the one-tap join it buys? The alternative is allowlists plus a push
   channel from the worker to every daemon, which we rejected as a standing dependency.
3. **The page holds PRF output.** (01) During a ceremony the page has `S` and `D`. With
   CSP `default-src 'none'` it cannot exfiltrate, but a page that has been replaced can
   also change its CSP. The mitigations are domain control, open source and a CI-pinned
   hash. Is a further step — e.g. the daemon publishing the expected page hash and the
   user's browser extension checking it — worth designing for, or is this the accepted
   residual as with every passkey RP?
4. **PRF availability.** (D4) Firefox, some password managers and older hardware keys do
   not support PRF. A user whose passkey lands in a non-PRF authenticator cannot use
   beam. Do we detect this at `init` and refuse before the credential is created, and is
   "use a different authenticator" an acceptable answer?
5. **No presence.** (03) The daemon dials offline peers on a 60 s backoff forever. For a
   laptop on battery with three offline peers that is a DERP round trip each per minute.
   Should there be a "give up until user action" state?
6. **Revoke while the worker is down.** (D7) The revocation is signed and applied locally
   immediately and gossiped on `sync`; the append to the log is queued. Is queueing right,
   or should `revoke` fail loudly so the owner knows other machines may not hear until
   they connect to this one?
7. **Default grant of all three kinds.** (D17) Every joined machine may open a shell on
   every other. Should `join` take `--grant msg` for a machine that should only report?
8. **`peerId` is 64 bits.** Second-preimage against a given id is infeasible; a collision
   among an attacker's own keys buys nothing because admission verifies the full key.
   Keep 16 hex characters for display, or widen?
9. **Windows.** Named pipe, ConPTY, no Windows CI. First release or later?
10. **Attach timeout.** (D14) Ten seconds after `pty.open`. Electron under load — enough?
11. **Label in the signed entry.** The label is signed at join and cannot change without
    a new entry. `peer rename` is local. Is a fleet-wide rename ever wanted, and if so is
    "tap to re-sign" acceptable for it?
12. **Directory retention.** Fleets idle for two years are deleted. Members hold
    everything, so the only loss is a machine joining after that. Right number?
