# beam

beam connects the machines one person owns and carries three kinds of stream between
them: a `pty`, an `exec` with an exit code, and durable `msg` envelopes. It replaces SSH
as the way one of your machines reaches another. It does not replace tmux, which still
holds processes and their identity, and it knows nothing about repositories, worktrees
or agents. Those live above it, in n10.

beam is a single Go binary: a daemon plus CLI subcommands, in the style of `tailscale`
and `tailcat`. The transport is [tailcat](https://github.com/tailscale/tailcat):
WireGuard tunnels with NAT traversal and DERP relay, without Tailscale's control plane.
Trust is rooted in one WebAuthn passkey per person; every membership and every
revocation is a statement that passkey signed directly, so only a tap can add or remove a
machine. The one hosted piece is an open-source Cloudflare Worker at `beam.n10.is` that
serves the passkey ceremony page and stores a directory it cannot read.

## Status

Design for a prototype. Nothing here is built yet and nothing is in use anywhere, so
there is no compatibility to keep: every choice below is made on its merits. These
documents are what to review before building starts.

## Documents

Read in order. Each file owns one concern and cross-references the others.

| # | File | Owns |
|---|------|------|
| 1 | [docs/01-model.md](docs/01-model.md) | The problem, the principles, the trust model and the threat model. What is out of scope. |
| 2 | [docs/02-identity.md](docs/02-identity.md) | The node key, what the passkey yields, `$BEAM_DIR`, signed membership entries and revocations, the encrypted directory, ceremonies, `init` / `join` / `revoke` / `reset`, sync. |
| 3 | [docs/03-transport.md](docs/03-transport.md) | Why one key means one stack, the Server plus ephemeral-key Clients topology, addresses, admission with the possession proof, lifecycle, DERP. |
| 4 | [docs/04-streams.md](docs/04-streams.md) | A stream is one TCP connection. Header, frames, `hello`, `sync`, `pty`, `exec`, `msg`. Grants. Injected environment. |
| 5 | [docs/05-mailbox.md](docs/05-mailbox.md) | The durable mailbox on SQLite: envelopes, sequence numbers, the transactions, outcomes, subscribers. |
| 6 | [docs/06-control-socket.md](docs/06-control-socket.md) | The daemon's local socket: NDJSON control requests, attach connections for byte streams, connect-or-spawn, the lock. |
| 7 | [docs/07-cli.md](docs/07-cli.md) | Every subcommand, its output, its exit codes. |
| 8 | [docs/08-desktop.md](docs/08-desktop.md) | What n10 desktop and n10 TUI do with the daemon, and what they stop doing. |
| 9 | [docs/09-build-and-distribution.md](docs/09-build-and-distribution.md) | Module layout, build flags, platform matrix, npm packages, the worker and its API, versioning, CI. |
| 10 | [docs/10-testing.md](docs/10-testing.md) | Unit tests, the in-process dev DERP, the software authenticator, the fake worker, and how n10's e2e suites drive a real daemon. |
| 11 | [docs/11-decisions.md](docs/11-decisions.md) | The decision register, the milestone gate, and the open questions. |

## One paragraph for the impatient

Every machine has one WireGuard node key; 128 bits of its hash is the machine's
`peerId`. The owner has one passkey. Joining is one tap on the new machine: the passkey
signs the machine's entry as a WebAuthn assertion and, in the same assertion, yields the
key that encrypts the fleet directory. The entry goes, encrypted, to an append-only log
on an open-source worker that verifies the assertion but cannot read the entry. The new
machine reads everyone's addresses from that log and dials them; each completes a
WireGuard handshake, receives the signed entry plus a proof that the dialer holds the
key it names, verifies, and pins. From then on nobody consults anyone. Streams are TCP
through the tunnel; the daemon exposes one local socket for the CLI, n10 desktop and
scripts. Revoking is another tap, pushed to connected peers at once and to the rest as
they connect.

## Licence

MIT, in [LICENSE](LICENSE).
