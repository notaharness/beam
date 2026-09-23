# 10 · Testing

Everything but the real browser and real authenticator runs offline: an in-process DERP
that also serves its DERP map, a test authenticator behind a build tag, an in-process fake
worker, and daemons that take `$BEAM_DIR`, the DERP map and the directory URL as
`control.Options` (the binary's `--derp-map` sets the map, and in a beamtest build
`--directory` the directory).

## Unit

`go test ./...`. Every table in the spec has a test: a table test where the table is a
list of cases, a scenario where it is a protocol (grants, outcomes, transactions). Vectors in
`internal/identity/testdata/`: fixed node keys, a fixed ES256 credential, canonical
statements, their hashes, valid assertions, and one failing vector per verifier check
(bad peerId, address key mismatch, zero PSK, wrong credential id, wrong origin, wrong
rpIdHash, UV unset, bad signature, revoked). Canonical JSON (JCS) has its own vectors.
HKDF derivations are pinned so a salt or info change fails loudly.

Property tests (`pgregory.net/rapid`): frame codec under arbitrary splits; header lines at
and over the cap; mailbox under random crash points between and inside the transactions
in [05](05-mailbox.md) (SQLite's commit hook turns a chosen commit into a rollback, and
the machine restarts). Found counterexamples become fixed cases.

The npm packages: `node --test` runs the shim with a shell script standing in for the
binary (arguments, exit code, `128+signal`, each forwarded signal, one that comes while
`spawn` runs), `npm/pack.mjs`
over stand-in binaries, and `npm/version.mjs` over stable, prerelease and noncanonical
tags.

## Dev DERP

`internal/devderp` copies tailcat's `runDevDERP` (in-process `derpserver` behind an
`httptest` TLS server, loopback STUN) and reproduces the test-network isolation from
tailcat's own `TestMain`, not only its environment variable. The harness starts one relay
and passes its map to every daemon via `--derp-map`. Forced relay paths are tested by
blocking UDP between daemons: `devderp.ForceRelay` sets tailscale's
`TS_DEBUG_ALWAYS_USE_DERP`, which gives every engine in the process no UDP socket and
is inherited by child processes. Each new `Client` then spends netcheck's 3 s UDP
timeout before it connects ([03](03-transport.md), Footprint).

## Test authenticator

Build tag `beamtest`. With `BEAM_TEST_AUTHENTICATOR=<json: credentialId, ES256 private
key, prf secret>` set, ceremony ops skip the browser: they produce a WebAuthn-shaped
assertion (correct `clientDataJSON` for origin `https://beam.n10.is`, `authenticatorData`
with `rpIdHash` and UV) and a PRF value computed the way the spec defines it, seal the
result to the ceremony's key as the page does, and write it to the ceremony's slot on the
daemon's directory worker. The daemon waits on the slot, opens and verifies it as it
would a phone's. Verification is not bypassed. This is a protocol
fixture, not proof of any real authenticator's behaviour.

## Browser ceremony

A Playwright test drives real Chromium against the daemon's ceremony URL with the page
served from `worker/public/` under its `_headers` and a CDP virtual authenticator with
PRF enabled: a `create`, then a `get` with the credential it made. The page's slot write
goes to the fake worker, or in CI's contract step to the real one under `wrangler dev`,
and the test checks that the page reports `201` and the ceremony opens, verifies and
completes, and that the page answering the `get` a second time is told it was already
answered. That is also the HPKE interoperability test: the page's
Web Crypto sealing against Go's `crypto/hpke`, which carries RFC 9180's vectors. This is
the one boundary the test authenticator cannot exercise.

## Fake worker

`internal/fakeworker` implements the routes, bearer check, assertion verification and
slots in memory. The `directory` and `ceremony` packages' tests run against it, and in CI
again against the real worker under `wrangler dev` (`BEAM_WORKER_URL`), so the fake
cannot drift.

## Worker

vitest in workerd (`worker/test/`), against D1 and the rate-limit binding as miniflare
provides them: every route's refusals and caps; for slots, a write then a read returns
the ciphertext, a second write is `409` before and after the read, a second read is
`410`, a read with any other key is `401`, a read waits for a write that comes while it
waits, a row past five minutes is gone to both routes and deleted by the next write, a
body or a ciphertext over its cap is `413`, and a client address past its rate is `429`.
The page's policy is recomputed from its script and style, and `connect-src` is the slot
prefix alone.

## Integration

Two to four daemons, temp `$BEAM_DIR`s, dev DERP, fake worker, test authenticator. The
daemons run in the test process (`control.Run`, built with the `beamtest` tag) and the CLI
is called as `cli.Main`; where a scenario needs a dialer that misbehaves or falls silent, a
bare member node (transport only) stands in for a daemon:

| Scenario | Proves |
|---|---|
| init, join | admission on first contact in both directions; both pinned; every result came through a slot |
| third joins while second is offline; second returns | second admits third directly and also learns it via sync; one row |
| second joins while worker withholds first's revocation of third | second admits third until the tombstone arrives by sync, then terminates; documents eventual revocation |
| revoke while all tunnels are up | peers refuse within the sync delta, no reconnect needed |
| revoke with worker down after the tap | local effect immediate; `published: "pending"`; append lands after worker returns |
| worker down during a ceremony | the ceremony waits on its slot and ends `ceremony-timeout`; nothing is committed |
| onlooker answers first | a second authenticator seals its own result to the slot before the owner: on an enrolled machine `revoke` ends `wrong-passkey` and commits nothing, and the owner's write is `409` |
| junk in a slot | a ciphertext that does not open under the ceremony's key ends it `ceremony-state` |
| leaked address, no entry | handshake completes, hello absent, closed within 5 s; 16-slot budget enforced |
| possession | valid entry, wrong MAC → `possession`; replay of hello from another tunnel refused |
| wrong passkey | assertion from a second credential → `wrong-passkey` at join; a revoke signed by an unrelated credential is refused |
| supersession | re-join with a new address; peers switch; older entry ignored |
| exec, pty | argv, stdin, stdout/stderr, exit, `cwd`, injected env, resize, kill on loss |
| grants | `msg` refuses `pty` on a live tunnel; `none` still syncs |
| mailbox | stored/delivered/rejected; crash between store and ack → duplicate suppressed; subscriber defer/ack; `send` while connected flushes immediately |
| junk in the log | valid assertion, unrelated blob → ignored |
| daemon lock | second daemon exits 1; connect-or-spawn loser connects to winner |
| shutdown ends sessions | an enrolled daemon in a process of its own (`beam daemon --directory`, a beamtest-only flag naming the fake worker) serves a pty and an exec whose processes ignore SIGHUP; after SIGTERM, both are dead when the daemon has exited |
| exit with parent | a parent process spawns `beam daemon --exit-with-parent` with a stdin pipe and is killed with SIGKILL: the daemon exits; a terminal, `/dev/null` or `--detach` with the flag is a usage error |
| reset | tunnels closed, fleet state gone, key kept, re-join works |

## n10 e2e

`desktop-e2e` runs a real second daemon with a seeded `$BEAM_DIR`, dev DERP and fake
worker; the app spawns its own daemon through the helper. Screenshots stay stable because
the seeded keys never change.

## Manual, per release

Real ceremonies on macOS Safari and Chrome on Linux through the opened browser, and on
iOS Safari and Android Chrome by scanning the terminal's QR code from a headless machine
(an SSH session), each with `init` on a fresh dir and `join` on a second, and one
`revoke`; a scan of the code from a second phone after the first answered shows the
page's "answered from another device"; check that the derived directory key matches
across create/get and local/hybrid. Real NAT traversal between two networks; `beam
peers` shows `direct` or `relay`.
