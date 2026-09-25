# beam

beam connects the machines you own. From one machine you can open a shell on another,
run a command there, or send it a message that waits until it can be delivered. One
passkey decides which machines belong to your fleet; no account or server does.

One Go binary is both the CLI and a daemon, which starts when a command needs it.
Machines reach each other over encrypted [tailcat](https://github.com/tailscale/tailcat)
tunnels, directly or through Tailscale's public tailcat relays, without a Tailscale
account.

beam is part of [notaharness](https://github.com/notaharness): the
[orchestra plugin](https://github.com/notaharness/plugins/tree/main/orchestra) uses it
to run coding agents in tmux sessions on other machines, but beam runs any program.
beam is in beta: expect breaking changes between versions.

## Install

On each machine (macOS or Linux, arm64 or x64):

```sh
npm install -g @notaharness/beam
beam version
```

You also need a passkey with the WebAuthn PRF extension, which the browser, operating
system and passkey provider must all support, and a browser with X25519 in Web Crypto:
for example Apple Passwords with Safari on iOS 18.4 or later, or Firefox 139 or later
on the desktop. The approval page says when the browser lacks X25519. A passkey without
PRF fails the command with `prf-unsupported`, which lists what vendors document.

## Use

### Create a fleet

On the first machine:

```sh
beam init --label laptop
```

beam prints a QR code and a link to an approval page at `beam.n10.is`, and opens the
page in a browser if the machine has one. Scan the code with your phone or use the
browser, and create the passkey there. Then approve the second link it prints with the
same passkey. It prints the fleet's fingerprint, `3f9a 0c4e 7d12 e805` here:

```
created fleet 3f9a 0c4e 7d12 e805  ·  this machine: laptop (b7f3 9a21 0c4e 55d1)
```

Keep the QR code to yourself: whoever answers it first decides the machine's fleet.
Approve only a page that names the machine you ran the command on.

### Add a machine

On the new machine:

```sh
beam join --label buildbox
```

Approve with the same passkey, as for `init`. Then compare the fleet fingerprint it prints with
`beam status` on the first machine. If they differ, this machine joined a different
fleet: run `beam fleet reset` on it and join again.

`beam peers` lists the other machines and whether they are connected. A command takes a
machine by its label, an alias you set with `beam peer alias`, or at least the first 8
hex digits of its id.

### Remove a machine

From any machine in the fleet:

```sh
beam revoke buildbox
```

This takes a passkey approval too. The machine you ran it on cuts buildbox off at once,
connected machines within seconds, and offline ones when they next connect.

### Limit a machine

By default every machine can open a shell on every other. To limit what buildbox can do
on this machine, run this here: `msg` allows only messages, `none` allows nothing, and
`all` is the default.

```sh
beam peer grant buildbox msg
```

### Run commands and shells

```sh
beam exec buildbox -- uname -a
beam exec buildbox --cwd '~/src/app' -- make test
beam connect buildbox
beam connect buildbox -- tmux new-session -A -s work
```

`exec` passes stdin through, streams stdout and stderr, and exits with the remote
command's status. `connect` opens your login shell, or the command after `--`, in a
terminal on the other machine. Run it again to reattach the tmux session after you
detach or lose the connection.

### Send messages

```sh
beam msg send buildbox --topic jobs 'build main'    # on laptop
beam msg listen --topic jobs laptop                 # on buildbox, from laptop only
```

A message to an offline machine is stored and delivered when it connects. `listen`
prints one message per line and removes each from this machine's mailbox once printed.
`beam msg queue` on the sender shows messages the recipient has not received yet.

## How it works

Each machine has a WireGuard node key, which is its identity. Your passkey signs an
entry for each machine it admits, and a revocation for each it removes. Through PRF, the
passkey also yields the key that encrypts your fleet's directory, an append-only log of
those entries kept by the worker at [beam.n10.is](https://beam.n10.is), free to use
while its database has room. The worker
stores only ciphertext and never carries your shells, commands or messages. A machine
reads the directory when it joins and when its daemon starts, then admits a peer only if
the peer shows a signed entry and proves it holds the node key the entry names. The
[trust model](docs/01-model.md) sets out what the worker and the approval page can and
cannot do.

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

Report vulnerabilities privately: [SECURITY.md](SECURITY.md).

## Licence

MIT, in [LICENSE](LICENSE).
