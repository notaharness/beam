# Security

## Supported versions

The version under npm's `latest` dist-tag receives fixes. A fix ships as a new version.

## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting: the repository's
Security tab, then "Report a vulnerability", or
<https://github.com/notaharness/beam/security/advisories/new>. Do not open a public
issue or pull request.

Include the version (`beam version`), the operating system, what an attacker needs, and
the steps that show it.

## What to expect

- An acknowledgement within 7 days.
- An assessment: whether it is in scope, and how severe.
- For a confirmed vulnerability, a fix in a new release and a published GitHub security
  advisory, crediting you unless you ask otherwise. We agree the disclosure date with
  you, and ask that you keep the report private until then.

## Scope

beam roots a fleet of machines in one passkey. The passkey signs every membership entry
and revocation, and its PRF output yields the key that encrypts the fleet's directory.
Each machine holds a node key, the directory key and a mailbox. The trust and threat
model is [docs/01-model.md](docs/01-model.md).

In scope, in this repository's code (the CLI and daemon, the worker at `beam.n10.is`
with its ceremony page, and the npm packages and release binaries):

- Adding a machine to a fleet, or removing one, without a passkey approval of that
  operation.
- A machine that is not a member getting past admission or opening a stream, or a
  revoked machine admitted by one that holds its revocation.
- A peer opening a shell or running a command on a machine that granted it only `msg` or
  `none`.
- Reading directory entries without the directory key, or a ceremony result without its
  ceremony's key, or forging either.
- A node key, the directory key, the mailbox or the control socket reachable by another
  local user.
- The ceremony page signing a statement other than the one it shows, or leaking the
  passkey's PRF output.
- The release workflow or the npm packages shipping something other than what this
  repository builds.

Out of scope, by design ([docs/01](docs/01-model.md#threat-model)):

- A member machine stolen or compromised before it is revoked, and what it did
  meanwhile. With the default grant, a member has a shell on every other member.
- A hostile ceremony page, from whoever controls what `beam.n10.is` serves: it can
  substitute the statement of the one approval it handles, and a machine's root at its
  first enrolment.
- Someone who scans a ceremony's QR code and answers before you, or a ceremony you did
  not start and approve anyway.
- Metadata visible to the DERP relays and the worker: sizes, timing, IP addresses, a
  fleet identifier.
- The availability of `beam.n10.is` or the DERP relays, and resource exhaustion by a
  holder of a machine's address beyond what tailcat bounds.
- A lost passkey with no synced copy.

Vulnerabilities in tailcat, a browser or a passkey provider belong with that project.
