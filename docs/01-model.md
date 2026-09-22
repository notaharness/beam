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
| Authentication | A knows the packets came from B and no one else; B likewise | WireGuard proves possession of a node key; a passkey-signed membership entry proves that key is the owner's |
| Confidentiality and integrity | No one on the path reads or alters a byte | WireGuard (Noise IK, ChaCha20-Poly1305) plus a per-server pre-shared key |

WireGuard supplies reachability, confidentiality and half of authentication: it proves
the far side holds the private key for the public key it presented. beam's own design
content is the other half — **how a machine knows that key belongs to the owner** — and
nothing else. Everything after that (streams, mailbox, socket) rides on an
authenticated byte pipe and would be the same over any transport.

## Principles

1. **Only a tap mints membership.** A machine is in the fleet if and only if the owner's
   passkey signed a statement saying so. No machine's disk holds anything that can add
   another machine. The same is true of removing one.
2. **One root.** The fleet is rooted in exactly one passkey. There is no key set, no
   list of admins, no second root to slip an entry into. Losing the passkey means a new
   fleet; the existing one keeps working meanwhile because trust, once verified, is
   pinned locally.
3. **No server in the trust path.** The one hosted component stores ciphertext it
   cannot read, containing statements it cannot forge, and is consulted only when a
   machine joins. Its code is open. Its worst case is refusing to serve.
4. **Verify on contact, pin forever.** A machine proves membership the first time it
   connects to another by presenting its signed entry. The receiver checks the signature
   once and stores the result. No hosted directory is consulted at runtime, ever.
5. **One way to do each thing.** One transport, one root, one socket, one binary. No
   fallbacks behind flags, no second implementation kept in case.
6. **One binary, one daemon per machine.** The CLI, n10 desktop and Orchestra scripts
   all connect to the same running daemon. A machine never runs two beam nodes against
   one `$BEAM_DIR`.

## Trust model

### Actors

- **The owner**, who holds the passkey (phone, laptop secure enclave, or a synced
  keychain) and taps to approve.
- **Machines**, each holding one WireGuard node key. The public half identifies it.
- **The passkey**, a WebAuthn credential scoped to the relying party `pair.n10.is`.
  Through the PRF extension it yields secrets that only the owner's authenticator can
  reproduce; from those beam derives the fleet's keys ([02-identity](02-identity.md)).
- **The membership key** `MPK`, an Ed25519 public key derived from the passkey. Its
  private half exists in a daemon's memory only during a ceremony. It signs membership
  entries and revocations.
- **The directory**, an append-only log of ciphertext entries per fleet, stored by an
  open-source Cloudflare Worker at `pair.n10.is`. It also serves the static ceremony
  page.
- **DERP relays**, Tailscale's public tailcat fleet or one we run. They forward
  encrypted packets between machines that cannot reach each other directly and act as
  the rendezvous for hole-punching.

### What each actor is authoritative for

| Actor | Authoritative for | Never authoritative for | If compromised |
|---|---|---|---|
| Node key | One machine's identity; every byte it sends | Membership | That machine is impersonated. Revoke it (a tap); nothing else is affected. |
| Passkey / `MPK` | Membership and revocation | Anything per connection | The attacker mints members. This is total, which is why the private half never touches a disk. The owner starts a new fleet. |
| Directory worker | Storing and returning ciphertext | Contents, membership, anything at runtime | It can refuse to serve, which blocks joining. It cannot read an address, forge an entry or remove one a machine already holds. It sees blob sizes, timing and a fleet identifier. |
| Ceremony page | Running the WebAuthn call honestly, once per tap | Anything after the redirect | During a ceremony it holds PRF output and could sign an attacker's key or leak the directory key. See "The one trusted moment". |
| A fleet machine's disk | Its own pins, its own mailbox, the cached directory key | Membership | Stolen: retains tunnel access until revoked; can read the directory (addresses and labels). Cannot add or remove a machine. |
| DERP relay | Delivery of encrypted packets; rendezvous | Contents; identity; membership | New connections stall while it is down. It cannot complete a handshake (no private keys) and cannot join a tunnel it observes (pre-shared key). |

### The one trusted moment

The ceremony page runs in the owner's browser at `https://pair.n10.is` and receives the
PRF output before handing it to the daemon over loopback. For those seconds it is
trusted. A hostile page could derive the membership key and sign an entry for a key of
the attacker's choosing, or keep the directory key. Bounds:

- The page is one static file with `Content-Security-Policy: default-src 'none'`, so it
  cannot send anything anywhere except the loopback redirect the daemon asked for. Its
  source is public and its hash is pinned in CI.
- The attacker would have to control the domain, not just observe it: a passkey scoped to
  `pair.n10.is` answers no other origin.
- The exposure lasts one ceremony; the derived private key is never cached anywhere.

This is the same moment of trust every passkey-based system has in its relying party.
It is smaller than the previous design's, where a worker held plaintext and a directory
was trusted at enrolment, and it is the residual cost of using WebAuthn at all.

### Threat model

**Defended against:**

- A passive or active attacker anywhere on the network path, including the DERP
  operator: WireGuard with the peer's key verified against a passkey-signed entry.
- The directory worker, hostile or breached: it holds ciphertext and cannot forge a
  signature. Deleting entries only blocks joining; machines already in the fleet hold
  their own copies and gossip them.
- A stolen or compromised fleet machine trying to add a machine: impossible without a
  tap. Trying to revoke others: impossible without a tap.
- A stolen machine after the owner notices: `beam revoke` (one tap) and every other
  machine refuses it on next contact and terminates any tunnel it has.
- A leaked machine address: the holder can complete a WireGuard handshake (the address
  carries the PSK) and reach beam's admission check, where it is closed for lacking a
  signed entry. Nuisance only.
- Lookalike domains: the passkey answers only `pair.n10.is`.

**Not defended against, by decision:**

- A stolen machine before the owner notices. It has the access it always had. Same as
  an SSH key on that machine.
- A hostile ceremony page during a ceremony (bounded above).
- Loss of the passkey with no synced copy. The fleet keeps working; adding or revoking
  needs a new fleet.
- Metadata visible to the DERP operator and the directory worker.
- A second human. No sharing, no guest access, no cross-fleet anything.

## Out of scope

- Relays beyond DERP.
- Multiple humans, guests, shared fleets, roles.
- Encrypting the mailbox at rest beyond `0600`.
- A browser client. The browser appears only as the host of one ceremony page.
- Interoperating with a user's existing Tailscale tailnet.
