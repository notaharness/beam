# beam

beam connects the machines one person owns and carries three kinds of stream between
them: a `pty`, an `exec` with an exit code, and durable `msg` envelopes. It replaces SSH
as the way one of your machines reaches another. It does not replace tmux, which still
holds processes and their identity, and it knows nothing about repositories, worktrees
or agents. Those live above it, in n10.

beam is a single Go binary: a daemon plus CLI subcommands, in the style of `tailscale`
and `tailcat`. The transport is [tailcat](https://github.com/tailscale/tailcat):
WireGuard tunnels with NAT traversal and DERP relay, without Tailscale's control plane.
Trust is rooted in one WebAuthn passkey per person; only a tap on it can add or remove a
machine. The one hosted piece is an open-source Cloudflare Worker that serves the passkey
ceremony page and stores a directory it cannot read.

## Status

Design for a prototype. Nothing here is built yet and nothing is in use anywhere, so
there is no compatibility to keep: every choice below is made on its merits. These
documents are what to review before building starts.

## Documents

Read in order. Each file owns one concern and cross-references the others.

| # | File | Owns |
|---|------|------|
| 1 | [docs/01-model.md](docs/01-model.md) | The problem, the principles, the trust model and the threat model. What is out of scope. |
| 2 | [docs/02-identity.md](docs/02-identity.md) | Keys derived from the passkey, `$BEAM_DIR`, signed membership entries, `init` / `join` / `revoke`, the ceremony page, the encrypted directory, gossip. |
| 3 | [docs/03-transport.md](docs/03-transport.md) | How tailcat is used: one `Server` and one `Client` per peer, addresses, admission on first contact, DERP, connection lifecycle. |
| 4 | [docs/04-streams.md](docs/04-streams.md) | A stream is one TCP connection through the tunnel. The open header, the frame format, `pty`, `exec`, `msg`, `sync`. Scopes. Injected environment. |
| 5 | [docs/05-mailbox.md](docs/05-mailbox.md) | The durable mailbox: envelopes, sequence numbers, both queues, both acknowledgements, outcomes, quarantine. |
| 6 | [docs/06-control-socket.md](docs/06-control-socket.md) | The daemon's local socket: NDJSON control requests, attach connections for byte streams, connect-or-spawn, the lock. |
| 7 | [docs/07-cli.md](docs/07-cli.md) | Every subcommand, its flags, its output, its exit codes. |
| 8 | [docs/08-desktop.md](docs/08-desktop.md) | What n10 desktop and n10 TUI do with the daemon, and what they stop doing. |
| 9 | [docs/09-build-and-distribution.md](docs/09-build-and-distribution.md) | Module layout, build flags, platform matrix, npm packages, the worker and its API, versioning, CI. |
| 10 | [docs/10-testing.md](docs/10-testing.md) | Unit tests, the in-process dev DERP, the software authenticator, the fake worker, and how n10's e2e suites drive a real daemon. |
| 11 | [docs/11-decisions.md](docs/11-decisions.md) | The decision register, and the open questions a reviewer should attack first. |

## One paragraph for the impatient

Every machine has one WireGuard node key; its public half, hashed, is the machine's
`peerId`. The owner has one passkey. Through WebAuthn's PRF extension it yields secrets
that derive a membership signing key, which exists only during a tap, and a directory
key, which every machine caches. Joining is one tap on the new machine: it signs its own
entry and appends it, encrypted, to a log on an open-source worker. Then it dials every
machine in the log; each completes a WireGuard handshake, reads the signed entry, checks
the signature, and pins it. From then on nobody consults anyone. Streams are TCP through
the tunnel; the daemon exposes one local socket so the CLI, n10 desktop and scripts share
it. Revoking is another tap. The worker holds ciphertext it cannot read and is never
touched at runtime.
