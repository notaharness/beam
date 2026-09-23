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
   so only a tap can append. It carries each ceremony's result back to the machine
   sealed to a key it never sees. It is read at join and at daemon start, written at init,
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
  worker verifies. The same worker serves the static ceremony page and relays each
  ceremony's sealed result from the page back to the daemon through a one-time slot.
- **DERP relays**, Tailscale's public tailcat fleet or one we run.

### What each actor is authoritative for

| Actor | Authoritative for | Never authoritative for | If compromised |
|---|---|---|---|
| Node key | One machine's identity | Membership | That machine is impersonated until revoked. A shell-capable member is the owner's OS account on every machine it reaches, so a stolen member that was used before revocation may have copied other keys; see "Blast radius". |
| Passkey | Membership and revocation, one statement per tap | Anything per connection | Total. The owner starts a new fleet. |
| Directory worker | Storing and returning ciphertext; refusing writes that lack a valid assertion; holding a ceremony's sealed result until its daemon reads it | Contents; membership; ceremony results; anything at runtime | Refuses to serve: every ceremony, and so init, join and revoke, stalls, publishing stalls, and a starting daemon learns no revocation from it. Nothing at runtime waits on it. Cannot read an address, forge an entry or remove one a machine already holds. Cannot open, alter or replay a ceremony result; can drop one, or fill a slot with junk, which ends that ceremony. Sees blob and result sizes, timing, a fleet identifier, and the addresses of the browser and the machine in a ceremony. |
| Ceremony page | Running one WebAuthn call honestly and sealing its result to the key in its URL; at a machine's first enrolment, that machine's root | Anything beyond the operation being approved | Can substitute the statement being signed during that one tap, and can keep the PRF output (directory read access). Cannot sign a later statement. At a machine's first enrolment (`init`, a fresh `join`) it can substitute the root that machine pins. |
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

### The relayed result

The page returns a ceremony's result through a slot on the worker, sealed with HPKE to
a key the daemon made for that ceremony and put in the URL's fragment
([02](02-identity.md), Ceremonies). That is what lets a phone that scanned the terminal
answer for a machine it cannot reach. The worker holds only ciphertext and never sees
the key, so it cannot read a result, forge one or move one to another ceremony. What
the relay changes is who can answer: **the URL is a capability to answer the ceremony**,
and it now works from any device that has it, not only from a browser on the machine.
Seeing it is enough; it names the slot and the key, and HPKE's base mode does not
authenticate the sender.

**An onlooker who answers first.** Someone who sees the QR code while it is on the
screen (over a shoulder, on a screen share, in a photo) can run the same page with a
passkey of their own and write to the slot before the owner does. The slot takes one
write, so exactly one of them lands, and the machine gets it.

- On a machine that holds its root, their answer fails verification under the pinned
  credential: the ceremony ends `bad-assertion` or `wrong-passkey`. Denial of one
  ceremony, nothing more.
- At a machine's first enrolment it is the root substitution above, done by the
  onlooker instead of the page: at `init` the machine pins their credential as its
  root; at a fresh `join` it joins their fleet on the real worker (anyone can register
  one). Either way their root can then admit machines of theirs, which get a shell on
  this one under the default grant.

The owner learns of it from their own page: their write is refused, and the page says
the ceremony was answered from another device and that the machine must be reset
(`beam fleet reset`) if it enrolled. The slot stays taken for the ceremony's five
minutes, so no write within the ceremony can be told it landed when it did not. The
fleet fingerprint check catches the same thing later. Nothing prevents it: binding the
answer to the machine needs a secret the onlooker cannot see, such as a code shown on
the phone and typed into the terminal, and beam leaves that out for now. Show the QR
code where only you can see it.

**A URL the owner did not start.** A ceremony URL or QR code someone else made (sent
as "scan this to finish setting up", or put in place of the real one) carries their key
and their slot, and whatever challenge and text they chose. If the owner approves it,
the sealed result goes to them: the PRF output, so `K_dir` and `T_read` and with them
the fleet's addresses and labels for as long as it exists, and an assertion over the
one statement the page showed. If that statement adds their machine, they append it and
the machine is a member, with a shell on every other. If they reuse the owner's real
challenge and only swap the key, they learn the directory key and can re-seal the
result to the real slot, so the ceremony completes and nothing looks wrong. This is the
hostile page's exposure from "The ceremony as the trusted moment", with the page
honest: one statement per tap, which the page displays, and permanent directory read.
The defence is the owner's: approve only a ceremony you started just now, whose action,
machine and fingerprint match what the terminal shows. The page says so.

**Loopback proved presence; the relay does not.** A result delivered to
`127.0.0.1` could only come from a browser that reaches the machine's loopback, which
proved the approver was at the machine or had forwarded its port, and made both attacks
above impossible. beam keeps one path, the relay, for every ceremony, the local browser
included:

- The exposure to a URL the owner did not start comes from the page being able to
  return a result through a slot at all. A loopback path kept beside it for machines
  with a browser would not remove that; it would only keep a second path.
- What loopback would still buy on a desktop is protection from an onlooker at first
  enrolment, where the URL opens in the local browser at once and is never drawn for a
  camera. That onlooker has to read a URL of several hundred characters off the screen
  and answer before the owner's first tap; the owner's page tells them if they did.
- A headless machine, which is where this is for, gets the relay either way.

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
worker (hostile or breached) for everything but availability and metadata, ceremony
results included; a stolen or
compromised member adding or removing machines; a stolen member after revocation
reaches each peer; a leaked address (the holder completes a handshake and is closed at
admission); lookalike domains.

**Not defended against, by decision:** a stolen member before it is revoked, and what it
did meanwhile; a hostile page during one ceremony, and at `init` for the root; an
onlooker who scans a ceremony's QR code and answers before the owner, which at a first
enrolment roots the machine in their fleet (the owner's page reports it); a ceremony URL
the owner did not start and approves anyway; loss of
the passkey with no synced copy; metadata at the relay and the worker; a second human;
transport-level resource exhaustion by an address holder beyond what tailcat itself
bounds.

## Out of scope

Relays beyond DERP; multiple humans; encrypting the mailbox beyond `0600`; a browser
client; interoperating with a user's tailnet; Windows in the first release.
