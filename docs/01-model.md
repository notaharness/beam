# 01 · Model

## The problem

One person owns several machines: a laptop, a desktop at home, a box in a rack, perhaps
a phone. They want any of those machines to open a shell on any other, run a command
there and get its exit code, and leave a message that is delivered when the other side
comes back. The machines sit behind arbitrary NATs, run no inbound listener anyone can
reach, and move between networks. Nothing about this requires a second human, and
beam does not support one: a fleet is one person's machines.

Three properties have to hold between any two machines in the fleet:

| Property | Meaning | Supplied by |
|---|---|---|
| Reachability | A can get packets to B without either knowing the other's address in advance | tailcat: STUN, disco hole-punching, DERP relay |
| Authentication | A knows the packets came from B and no one else; B likewise | WireGuard with pinned node keys, both directions |
| Confidentiality and integrity | No one on the path reads or alters a byte | WireGuard (Noise IK, ChaCha20-Poly1305) plus a per-server pre-shared key |

WireGuard supplies all three once each side holds the other's public key. beam's own
design content is therefore small and precise: **how the right public keys get onto the
right machines, and nothing else.** That is the trust model below. Everything after it —
streams, mailbox, socket — rides on an authenticated byte pipe and would be the same
over any transport.

## Principles

1. **No server in the trust path.** No hosted component may ever be in a position to
   introduce a machine into the fleet or to read or alter a stream. The one hosted piece
   is a static page that performs a WebAuthn ceremony in the user's browser and holds no
   state; it is discussed in [02-identity](02-identity.md) and its worst case is a wasted
   tap.
2. **One root.** The fleet is rooted in exactly one passkey. There is no key set, no
   list of admins, no second root to slip an entry into. Losing the passkey means
   registering a new one and re-adding machines; the existing fleet keeps working
   meanwhile because trust, once established, is pinned locally.
3. **Trust once, pin forever.** A machine verifies another's attestation once, stores
   `(peerId, nodePublic, attestation)`, and never asks anyone about that peer again. The
   passkey is consulted only when a machine is added; no hosted directory is consulted
   ever.
4. **The tunnel authenticates; nothing else signs.** Because every byte that arrives
   through a tunnel is known to come from the pinned peer on the other end, control
   messages between peers — revocations, directory updates, labels — need no signatures
   of their own. The only signature in the system is the passkey's over an attestation.
   This is why there is exactly one key per machine.
5. **One way to do each thing.** One transport, one root, one socket, one binary. No
   fallbacks behind flags, no second implementation kept in case.
6. **One binary, one daemon per machine.** The CLI, n10 desktop and Orchestra scripts
   all connect to the same running daemon. A machine never runs two beam nodes against
   one `$BEAM_DIR`.

## Trust model

### Actors

- **The owner**, who holds the passkey (in a phone, a laptop's secure enclave, or a
  synced keychain) and who is the out-of-band channel: they paste addresses and tap to
  approve.
- **Machines**, each holding one WireGuard node key. The public half identifies it.
- **The passkey**, a WebAuthn credential scoped to the relying party `pair.n10.is`. It
  signs attestations and does nothing else.
- **DERP relays**, Tailscale's public fleet or one we run. They forward encrypted
  packets between machines that cannot reach each other directly and act as the
  rendezvous point for hole-punching.
- **The ceremony page**, one static HTML file at `https://pair.n10.is`. It runs
  `navigator.credentials.create` / `.get` and redirects the result to a loopback port.

### What each actor is authoritative for

| Actor | Authoritative for | Never authoritative for | If compromised |
|---|---|---|---|
| Node key | The identity of one machine; every byte it sends | Fleet membership | That one machine is impersonated. Revoke it; nothing else is affected. |
| Passkey | Fleet membership: it signs one attestation per machine | Anything per connection | The attacker can attest their own machine, but no existing machine will dial it or allow it until the owner pastes its address into `beam add`. The owner re-registers and re-adds. |
| DERP relay | Delivery of encrypted packets; rendezvous | Contents; identity; membership | New connections stall while it is down. It cannot complete a handshake (no private keys) and cannot join a tunnel it observes (pre-shared key). It sees metadata: public keys, timing, byte counts. |
| Ceremony page | Running the WebAuthn call honestly | State; verification; anything after the redirect | It can make the authenticator sign a different challenge than the daemon asked for. The daemon then rejects the result. The attacker holds an attestation for a key nobody allowlists. |
| A fleet machine's disk | Its own pins, its own mailbox | Other machines' pins | Stolen: retains tunnel access until revoked; revocation is immediate locally and propagates on next contact. Cannot add a machine — that needs the passkey. |

### The one trusted moment

When a machine joins, it learns the passkey's public key from the machine that
introduces it, through the tunnel it just opened to the address the owner pasted. That
is the only moment a machine trusts something it cannot yet verify. It is bounded:

- The introducer is authenticated by the pasted address, which contains its node key.
  The newcomer is talking to whichever machine the owner pointed it at.
- If that introducer lies about the passkey public key, the newcomer will accept only
  machines attested by the lie and refuse every real fleet member. It cannot be made to
  trust an attacker's fleet *and* the owner's. The desktop shows this state as "this
  machine's trust root does not match your other machines"; nothing depends on anyone
  reading that.

### Threat model

**Defended against:**

- A passive or active attacker anywhere on the network path, including the DERP
  operator: WireGuard with pinned keys on both sides.
- A hostile or compromised ceremony page: the daemon verifies challenge, origin,
  `rpIdHash` and signature; a wrong challenge is rejected; the page has no network access
  (see [02-identity](02-identity.md), CSP).
- A stolen machine, after the owner notices: `beam revoke` removes it from every
  allowlist the revocation reaches, and existing streams from it are terminated.
- Address pasted into the wrong machine: the holder can connect once, receives no
  attestation (the owner sees an unexpected fingerprint in the approval prompt and
  declines), and the join address is single-use.
- Lookalike domains: a passkey scoped to `pair.n10.is` produces no assertion for any
  other origin.

**Not defended against, by decision:**

- A stolen machine before the owner notices. It has the same access it always had. This
  is the same posture as an SSH key on that machine.
- A compromised machine acting as introducer for a *new* machine. It cannot: the
  attestation needs a passkey tap on a device with the authenticator. It can, however,
  revoke other machines (anyone in the fleet may), which is disruption, not access.
- Loss of the passkey with no synced copy. The fleet keeps working; adding a machine
  requires re-registering and re-adding all machines under the new root.
- Metadata visible to the DERP operator.
- A second human. There is no sharing, no guest access, no cross-fleet anything.

### What a compromised component cannot do

The property every change must preserve, stated once: **compromising any single
component yields at most that component's own column in the table above.** A DERP that
turns hostile cannot become an introducer. A page that turns hostile cannot become a
relay. A machine that is stolen cannot mint another.

## Out of scope

- TURN-style relays beyond DERP. DERP is the relay of last resort; there is no second one.
- Multiple humans, guests, shared fleets, roles.
- Encrypting the mailbox at rest beyond `0600`.
- A hosted directory of machines. The fleet directory travels peer to peer.
- A browser client. beam runs on Node-less machines; the browser appears only as the
  host of one ceremony page.
- Interoperating with a user's existing Tailscale tailnet. tailcat is deliberately
  separate from tailscaled and beam follows that.
