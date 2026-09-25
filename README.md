# beam

beam is a simple utility built on [tailcat](https://github.com/tailscale/tailcat) that
lets you pool the machines you own. Open a shell, run a command, or send a durable
message from one machine to another. A single Go binary provides the daemon and CLI.

It pairs naturally with tmux: keep an agent session running on another machine and
attach when you need it. beam handles reaching the machine; tmux keeps the session.
You can offload work from your laptop without moving the agent's working directory or
process back and forth.

beam is part of [notaharness](https://github.com/notaharness), a project making small,
reusable components for working with agents. The
[orchestra plugin](https://github.com/notaharness/plugins/tree/main/orchestra) is one
way to use it: orchestra gives each agent a tmux session and a git worktree, with
conventions for reporting to a supervising conversation. beam mandates no agent,
orchestrator or program. Use it with those conventions, your own scripts, or an
interactive shell.

## Pairing and privacy

The Cloudflare worker at [beam.n10.is](https://beam.n10.is) is free for anyone to use
for pairing their machines, for as long as its 10 GB D1 database has capacity. It serves
the passkey ceremony page and an encrypted, append-only directory.

Your machines' directory entries are encrypted with keys derived from **your passkey**.
The worker stores ciphertext it cannot read. Shells, commands and messages travel
between your machines over encrypted tunnels; the directory worker does not carry
them. The [trust model](docs/01-model.md) describes what the service can see and what
you trust during a pairing ceremony.

## Install and quick start

The implementation is a prototype. Install the npm package on each machine (macOS or
Linux, arm64 or x64):

```sh
npm install -g @notaharness/beam
```

On the first machine, create your fleet and its passkey:

```sh
beam init --label laptop
```

On the second, join using the same passkey:

```sh
beam join --label buildbox
```

After joining, compare the `fleet xxxx xxxx xxxx xxxx` fingerprint in the output with
`beam status` on the first machine. They must match. If they differ, run
`beam fleet reset` on the joining machine.

The commands open a browser when the machine has one, and print the ceremony URL, with a
QR code above it on a terminal. On a headless machine, scan the code with your phone and
approve there; the result comes back through `beam.n10.is`, sealed to a key only the
daemon holds. Anyone who scans the code can answer it too, so show it only where you
alone can see it, and approve only a page that names the machine you are enrolling.

The daemon starts when needed; `beam peers` shows whether the machines have connected.

From the laptop:

```sh
beam peers
beam connect buildbox
beam exec buildbox -- uname -a
beam msg send buildbox --topic jobs 'ready'
```

On buildbox, `beam msg listen --topic jobs laptop` receives messages from the laptop.
Messages are queued durably when they cannot be delivered immediately.

With tmux installed on buildbox, start or reattach an agent's session there:

```sh
beam connect buildbox -- tmux new-session -A -s agents
```

Run your agent inside that session. Detach from tmux and reconnect with the same
command; the session stays on buildbox. See [the CLI](docs/07-cli.md) for grants,
revocation, queue inspection and the other commands.

## Technical gist

Each machine keeps a WireGuard node key; 128 bits of its hash form its `peerId`.
The fleet's passkey signs membership entries and revocations directly as WebAuthn
assertions. A `get` ceremony also evaluates the passkey's PRF, from which the daemon
derives the directory encryption key and read token. Entries go into the worker's
encrypted, append-only directory, read at join and daemon start. tailcat supplies NAT
traversal and DERP relay without Tailscale's control plane: each machine runs a Server
with its node key and dials through Clients with ephemeral keys. On first contact, the
receiver verifies the signed entry and proof of possession of the node key it names,
then admits and pins the peer. No directory request is needed per connection. TCP
streams inside the tunnels carry `pty`, `exec` and durable `msg` envelopes; a local
socket exposes them to the CLI and other programs. Revocations propagate over live
peer connections and through the directory to machines that were offline.

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

## Licence

MIT, in [LICENSE](LICENSE).
