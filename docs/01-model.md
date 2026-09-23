# 01 · Model

## The problem

One person owns several machines: a laptop, a desktop at home, a box in a rack. They
want any of those machines to open a shell on any other, run a command there and get
its exit code, and leave a message that is delivered when the other side comes back.
The machines sit behind arbitrary NATs, run no inbound listener anyone can reach, and
move between networks. Nothing about this requires a second human, and beam does not
support one: a fleet is one person's machines.

| Property | Meaning | Supplied by |
|---|---|---|
| Reachability | A can get packets to B without either knowing the other's address in advance | tailcat: STUN, disco hole-punching, DERP relay |
| Authentication | A knows the packets came from B and no one else; B likewise | WireGuard proves possession of a key; a passkey assertion over B's entry proves that key is the owner's |
| Confidentiality and integrity | No one on the path reads or alters a byte | WireGuard (Noise IK, ChaCha20-Poly1305) plus a per-server pre-shared key |

WireGuard supplies reachability, confidentiality and half of authentication. beam's
design content is the other half, **how a machine knows a key belongs to the owner**,
and nothing else. Streams, mailbox and socket ride on an authenticated byte pipe and
would be the same over any transport.

## Principles

1. **Only a tap mints or removes membership.** A machine is in the fleet if and only if
   the owner's passkey has signed a statement naming its key. The passkey signs each
   such statement directly, as a WebAuthn assertion whose challenge is the statement's
   hash. No derived signing key exists anywhere, so no machine, page or process can sign
   a second statement with what it saw during a first.
2. **One root.** Exactly one passkey. No key set, no admin list, no second root. Losing
   it means a new fleet; the old one keeps working because trust, once verified, is
   pinned locally.
3. **No server in the trust path.** The one hosted component stores ciphertext it cannot
   read containing statements it cannot forge, and verifies passkey assertions on writes
   so only a tap can append. It is read at join and at daemon start, written at init,
   join and revoke, and never polled or consulted per connection. Its code is open.
4. **Verify on contact, pin forever.** A machine proves membership the first time it
   connects to another by presenting its signed entry and proving possession of the key
   the entry names. The receiver checks once and stores the result.
5. **One way to do each thing.** One transport, one root, one socket, one binary, one
   store. No fallbacks behind flags, no second implementation kept in case.
6. **One daemon per machine.** The CLI, n10 desktop and Orchestra scripts connect to the
   same running daemon. A machine never runs two against one `$BEAM_DIR`.

## Trust model

### Actors

- **The owner**, who holds the passkey and taps to approve.
- **Machines**, each with one WireGuard node key. Its public half identifies the machine.
- **The passkey**, a WebAuthn credential scoped to `beam.n10.is`. It signs membership
  entries and revocations, and via the PRF extension yields the directory key.
- **The directory**, an append-only log per fleet held by an open-source Cloudflare
  Worker at `beam.n10.is`. Entries are ciphertext; appends carry a passkey assertion the
  worker verifies. The same worker serves the static ceremony page.
- **DERP relays**, Tailscale's public tailcat fleet or one we run.

### What each actor is authoritative for

| Actor | Authoritative for | Never authoritative for | If compromised |
|---|---|---|---|
| Node key | One machine's identity | Membership | That machine is impersonated until revoked. A shell-capable member is the owner's OS account on every machine it reaches, so a stolen member that was used before revocation may have copied other keys; see "Blast radius". |
| Passkey | Membership and revocation, one statement per tap | Anything per connection | Total. The owner starts a new fleet. |
| Directory worker | Storing and returning ciphertext; refusing writes that lack a valid assertion | Contents; membership; anything at runtime | Refuses to serve: init, join and publishing stall, and a starting daemon learns no revocation from it. Nothing at runtime waits on it. Cannot read an address, forge an entry or remove one a machine already holds. Sees blob sizes, timing and a fleet identifier. |
| Ceremony page | Running one WebAuthn call honestly; at a machine's first enrolment, that machine's root | Anything beyond the operation being approved | Can substitute the statement being signed during that one tap, and can keep the PRF output (directory read access). Cannot sign a later statement. At a machine's first enrolment (`init`, a fresh `join`) it can substitute the root that machine pins. |
| A fleet machine's disk | Its pins, its mailbox, the directory key | Membership | Stolen: tunnel access until revoked; permanent read access to the directory. Cannot add or remove a machine. |
| DERP relay | Delivery of encrypted packets; rendezvous | Contents; identity; membership | New connections stall while down. Cannot complete a handshake or join a tunnel it observes. |

### The ceremony as the trusted moment

During a tap the page runs in the owner's browser at `https://beam.n10.is`. It could
show "add buildbox" while asking the authenticator to sign a different entry. From that
one tap a hostile page obtains an assertion over a statement of its choosing and the PRF
output. The PRF output yields the read token and the directory key, and with them the
page can append that one entry, or that one revocation, to the directory itself: add a
machine it holds the key of, or remove one of the owner's. It gets one statement per
tap and none after: there is no seed to keep and no future signing authority. What it
keeps is the directory key, which reads addresses and labels for as long as the fleet
exists. That is the full exposure on a machine that already holds its fleet's root, and
it is accepted: this is the relying-party trust every passkey system has, bounded to one
operation.

A machine's first enrolment is where the page is trusted for more. At `init`, `create`
is where the root comes from, and beam checks no attestation (verifying one would mean
trusting an attestation CA, which beam does not): a hostile page can return a credential
it holds itself, the machine pins it as the fleet's root, and the page can then sign any
statement for as long as the fleet exists. A fresh `join` has no root to check its
answer against either: a hostile page can return an assertion and a PRF output of its
own, a directory that goes along presents the matching credential, and the machine pins
a root the attacker holds. The owner's fleet is untouched, its root still the owner's,
but the joining machine is in the attacker's fleet, whose root can add machines it will
admit. So the page is trusted for the root once per machine, at its first enrolment. The
check that does not pass through the page is the fleet fingerprint, 64 bits of
`fleetId`, which `init`, `join` and `beam status` print alike ([07](07-cli.md)): a
machine whose `join` names a fleet other than the one `beam status` shows on a machine
already in it joined someone else's, and is reset. Every later ceremony on an enrolled
machine is bound to its pinned root: a re-join and a revoke verify under the cached
credential and never take one from the page or the worker. The page has
`Content-Security-Policy: default-src 'none'`, its source is public and its hash is
pinned in CI; none of that is a cryptographic guarantee, and the spec does not claim one.

### Blast radius

The default grant gives every member a shell on every other member as the daemon's
user. That user can read `$BEAM_DIR` on that machine. So a member that is stolen and
*used* before it is revoked can have copied every reachable machine's node key and
directory key. Revoking the stolen machine then evicts its key, not an attacker who
already holds others. The honest statement is: a shell-capable member is transitively
the owner's account across the fleet, and a fleet that has been compromised that way is
recovered by starting a new one. The per-machine grant `msg` exists for a machine that
should only report; it is a real restriction on *that* machine's opens on the machines
that set it, and nothing more.

### Revocation is eventual

A revocation takes effect on the revoking machine immediately, on peers with a live
tunnel within seconds, in the directory when the append succeeds, and on offline peers
when they next connect to anyone who knows. A fresh machine that reads the directory
while the worker is withholding a tombstone will admit the revoked key until it learns
otherwise. There is no freshness authority; adding one would make the worker a runtime
dependency. beam reports what it knows: revoked here, published or pending, acknowledged
by N peers.

### Threat model

**Defended against:** any network attacker including the DERP operator; the directory
worker (hostile or breached) for everything but availability and metadata; a stolen or
compromised member adding or removing machines; a stolen member after revocation
reaches each peer; a leaked address (the holder completes a handshake and is closed at
admission); lookalike domains.

**Not defended against, by decision:** a stolen member before it is revoked, and what it
did meanwhile; a hostile page during one ceremony, and at `init` for the root; loss of
the passkey with no synced copy; metadata at the relay and the worker; a second human;
transport-level resource exhaustion by an address holder beyond what tailcat itself
bounds.

## Out of scope

Relays beyond DERP; multiple humans; encrypting the mailbox beyond `0600`; a browser
client; interoperating with a user's tailnet; Windows in the first release.
