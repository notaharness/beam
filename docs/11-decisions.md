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
| D23 | A `Makefile` with `dist`, `test`, `crap`, `lint` is the one entry point for local and CI builds; `golangci-lint` is the one linter and is run by `go run` at a pinned version. | Three shell lines do not need a Go build tool; `go run` needs no install step and builds the linter with the module's Go. | 09 |
| D24 | Per-function CRAP ≤ 30 gates CI, computed by `tools/crap` from the module-wide coverage profile. | Coverage alone rewards testing trivial code; CRAP demands tests where complexity is. A hundred-line tool beats a dependency. | 09 |
| D25 | A hello that ends without a verdict (deadline passed, dialer left) leaves its tunnel unbound rather than failed. | Failure marks a refused proof or an eviction. A stalled hello has already spent its 5 s and its budget slot, and a fresh tunnel would cost the dialer no more. | 03 |
| D26 | The acceptor retires a tunnel, closing every stream it carried, when its inbound `sync` ends or is silent 30 s. | The dialer keeps a tunnel exactly as long as its `sync`; a stream's own close can be lost with the tunnel, and a remote shell must not outlive its opener. | 03 |
| D27 | A `pty` or `exec` without `cwd` starts in the acceptor's home directory. | Where a login shell starts; the opener's directory means nothing on another machine. | 04 |
| D28 | tailcat's own logs are discarded; `daemon.log` holds beam's lines only. | tailcat's logs are verbose and not actionable for a beam user. | 06 |
| D29 | `status.derp.source` is `"key.json"`. | The relay region is fixed by the node key (D21); there is no other source yet. | 06 |
| D30 | A `pty`/`exec` leader is reaped only after its group's teardown, under the lock its signals take; the acceptor reads its exit status without reaping it (`waitid` `WNOWAIT`'s siginfo on Linux; on Darwin kqueue `NOTE_EXIT` with `NOTE_EXITSTATUS`, or the zombie's `p_xstat` for a leader that exited before the watch). A leader it cannot watch has its group killed, then is reaped for its status. | The unreaped leader holds the group id, so the SIGKILL reaches descendants that outlive it and never a group that reused the id. `/proc/<pid>/stat`'s `exit_code` reads 0 for a non-dumpable process. | 04 |
| D31 | `pty`/`exec` input is flow-controlled: at most 4 frames outstanding, each answered `taken`; an overrun closes the stream `window`. The daemon holds its attach clients to the same window. | A reader that stops reading to push back hides the opener's close behind queued input. With a window every reader reads on, so a close, a detach or a departure is always seen, as with SSH channel windows. | 04, 06 |
| D32 | `msg.ack` and `msg.defer` take `envelopeId`, and `msg.subscribe`'s `from` is a list. | Every control request already has an `id`; a subscriber filtering by sender may want several. | 05, 06 |
| D33 | A deferred envelope is offered again only to subscriptions made after the defer; `refused` in `msg.queue` is the deferred inbound mail. | The next subscribe gets it again, as 05 says, without a separate refused table; two subscribers cannot pass it back and forth. | 05, 06 |
| D34 | A serialized envelope is at most 960 KiB, a `msg.defer` reason 1 KiB, and `msg.queue` pages stop short of 1 MiB. JSON lines escape neither markup nor U+2028/U+2029, so an envelope never goes out larger than stored. | Every envelope then fits one 1 MiB control line with the `mail` event or `msg.queue` reply around it; escaping would swell `<` sixfold and a line separator twofold past it. | 05, 06 |
| D35 | A control client that leaves a line unread for 10 s is disconnected; a flusher send the peer leaves untaken for 30 s drops its msg stream for a new one. | A reader that stopped holds neither the daemon's other clients nor a peer's queue. | 05, 06 |
| D36 | `GET /v1/entries` names the fleet by `T_read` alone: `read_hash` is unique and the response carries `fleetId`. Every token that opens no fleet, malformed or unknown, gets the same `401`. | A joining machine holds only what its `get` yields; one refusal for every token says nothing about which fleets exist. | 02, 09 |
| D37 | An append carries `kind`, and the worker verifies the assertion under that one domain. | The daemon knows what it signed; trying both domains verifies twice for nothing. | 02, 09 |
| D38 | `acknowledgedBy` counts the peers whose pong, to a ping sent after the revocation delta, arrives within 5 s. | `sync` is ordered, so the pong proves the delta frame was read; no acknowledgement message is added. | 02, 06 |
| D39 | A ceremony's result travels in the fragment as `result` and the WebAuthn call's output, each field unpadded base64url; the daemon verifies a `create` itself. A result that does not verify, like `failed`, is `bad-assertion`. | The fields a verifier needs, encoded as every binary field is; nothing is trusted because the page sent it, and no valid assertion came back either way. | 02, 06 |
| D40 | `--label` defaults to the host name up to its first dot, cut to 64 characters, and `--fleet-name` to `beam`. | Both are display text; a default beats a prompt. A full host name can pass 64 characters (CI runners do). | 02, 07 |
| D41 | `beam join` on an enrolled machine re-enrols with the same passkey, homing the node key on the daemon's DERP map when its region is not on it. | 03's move to another DERP map is a re-join; the key, and so the peer id, stays. | 02, 03 |
| D42 | The daemon takes its DERP map and directory URL as `control.Options`; the binary sets only the DERP map (`--derp-map`). | Tests need both; a user changes only the relay (D21). | 03, 10 |

## Milestone gate

Before anything else: a spike with three daemons on the dev DERP with forced relay,
proving D6 (no collision with ephemeral client keys, possession MAC, admission), and
measuring a five-peer fleet's RSS and connection times. If it fails, the fallback is a
single stack per machine with an upstream `Server.Dial` contribution, or one tunnel per
pair with a multiplexer. Do not build on the topology until the spike passes.

**Passed 2026-09-22** (`internal/transport`, `TestTopology`, `TestFootprint`). Three
machines on one dev relay with UDP blocked, each one `Server` on its node key plus a
`Client` per peer on a fresh key, dial all six directed tunnels before using any, then
complete one round trip on each; the acceptor attributes every stream to the right
dialer. The same holds for five machines in five processes (20 tunnels). No DERP
collision appears. The possession MAC refuses a wrong MAC and a hello replayed from
another tunnel. Footprint numbers are in [03](03-transport.md).

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
