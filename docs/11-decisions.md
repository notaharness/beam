# 11 · Decisions and open questions

## Register

| # | Decision | Why | Where |
|---|---|---|---|
| D1 | Single Go binary, daemon plus CLI. | tailcat is a Go library; the process owning the tunnel must own admission. | 01, 09 |
| D2 | tailcat for transport. | WireGuard, NAT traversal and DERP with no control plane. | 03 |
| D3 | One node key per machine; `peerId` is 128 bits of its hash. | Tunnels authenticate everything a machine sends; a second key would sign nothing that needs it. Ids key storage, so 64 bits is not enough. | 02 |
| D4 | The passkey signs each membership and revocation statement directly as a WebAuthn assertion. | Only a tap can mint. No derived signing key exists to copy. Verifiers need a WebAuthn library; that is the price. | 01, 02 |
| D5 | PRF yields only the directory key and read token. | Their compromise reads addresses, not membership. | 02 |
| D6 | One networking stack per key: one `Server` with the node key, one `Client` per peer with an ephemeral key; possession proven by X25519 MAC in `hello`. | tailcat stacks sharing a key collide at DERP. | 03 |
| D7 | Streams are directional; each tunnel carries only its dialer's opens. | One owner per queue and per exchange. | 03, 04 |
| D8 | Admission at the beam layer on first contact; tailcat allow-any. | One-tap join with no polling or push. Transport-level exposure accepted. | 03 |
| D9 | Directory is an encrypted append-only log; appends require a passkey assertion the worker verifies; reads use a bearer token. | Only a tap writes; revoked machines cannot exhaust it; the worker never reads plaintext. | 02, 09 |
| D10 | Revocation is passkey-signed, permanent, eventual. Pushed on live `sync`, appended with retry, learned by others on contact. | Only the owner revokes; no freshness authority without a runtime server dependency. | 01, 02 |
| D11 | `sync` is long-lived and carries deltas and liveness. | Revocations must reach connected peers without a reconnect. | 04 |
| D12 | One TCP connection per stream; five-byte frames after a JSON header; one port. | netstack gives TCP; `pty`/`exec` need in-band control. | 04 |
| D13 | One grant with three values; `pty` and `exec` are one privilege. | They are the same shell. | 04 |
| D14 | Default grant `all`; a shell-capable member is transitively the owner's account. | One human. Stated, not hidden. | 01 |
| D15 | SQLite for peers, revocations, mailbox and pending writes. | Durability with transactions instead of a filesystem protocol. | 05 |
| D16 | Daemon outlives clients; only explicit stop stops it; one connect-or-spawn helper. | A listener that dies with the UI is not a listener. | 06 |
| D17 | Attach before anything runs remotely. | An expired reservation must not have side effects. | 06 |
| D18 | No `forget`; aliases are local; labels immutable per entry. | Forget fought admission and gossip. | 02, 07 |
| D19 | Unix first. | No Windows runner; ConPTY and pipes untested. | 09 |
| D20 | Lost passkey or compromised fleet → `fleet reset` and a new fleet. | No root migration path that a peer could abuse. | 02 |
| D21 | Public tailcat DERP by default; own relay via flag; relay change is a re-join. | Zero infrastructure; signed addresses need a signed update. | 03 |
| D22 | Domain `beam.n10.is` for the relying party and the worker. | One name for the one hosted thing. | 01, 09 |
| D23 | A `Makefile` with `dist`, `test`, `lint` is the one entry point for local and CI builds; `golangci-lint` is the one linter and is run by `go run` at a pinned version. | Three shell lines do not need a Go build tool; `go run` needs no install step and builds the linter with the module's Go. | 09 |

## Milestone gate

Before anything else: a spike with three daemons on the dev DERP with forced relay,
proving D6 (no collision with ephemeral client keys, possession MAC, admission), and
measuring a five-peer fleet's RSS and connection times. If it fails, the fallback is a
single stack per machine with an upstream `Server.Dial` contribution, or one tunnel per
pair with a multiplexer. Do not build on the topology until the spike passes.

## Open questions

1. **Possession proof via X25519 MAC.** (D6) Sound in principle (static-static DH with
   both keys public in the fleet); the MAC binds the ephemeral key from the tunnel. Is
   there a reason to prefer an upstream single-stack design even if the spike passes?
2. **Transport exposure under allow-any.** (D8) tailcat keeps peer state for any client
   that handshakes; beam bounds its own admission work but cannot evict transport state.
   Acceptable for a personal fleet, or should the spike measure the cost of N garbage
   handshakes and set a threshold?
3. **Eventual revocation.** (D10) Stated honestly now. Is "acknowledged by n peers" the
   right thing to show, and should `revoke` wait for any acknowledgements before
   returning?
4. **PRF matrix.** (D5) The spec now derives only from `get` and checks consistency. Which
   authenticator tuples do we commit to testing per release?
5. **Two taps at init.** (D4) `create` then `get`. A page that chains them saves nothing
   for authenticators that need UV per operation. Keep.
6. **Directory read access for revoked machines.** (D9) Permanent by design; rotating
   `K_dir` would need a re-join everywhere. Accept?
7. **Liveness numbers.** 15 s ping, 30 s timeout, 2 s → 5 min backoff, 20 s dial deadline.
   Measure on battery before fixing.
8. **`msg listen` acks on stdout write.** Acceptable for an observer; state it.
