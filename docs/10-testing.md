# 10 · Testing

Everything but the real browser and real authenticator runs offline: an in-process DERP
that also serves its DERP map, a test authenticator behind a build tag, an in-process fake
worker, and daemons that take `$BEAM_DIR`, the DERP map and the directory URL as
`control.Options` (the binary's `--derp-map` sets the map).

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
with `rpIdHash` and UV) and a PRF value computed the way the spec defines it, then POST
to the daemon's own `/cb/result`. Verification is not bypassed. This is a protocol
fixture, not proof of any real authenticator's behaviour.

## Browser callback

A Playwright test drives real Chromium against the daemon's ceremony URL with the page
served from `worker/public/` and a CDP virtual authenticator with PRF enabled, and checks
that `/cb` lands the fragment and the daemon completes. This is the one boundary the test
authenticator cannot exercise.

## Fake worker

`internal/fakeworker` implements the routes, bearer check and assertion verification in
memory. A contract test runs the Go client against the real worker under miniflare in
CI so the fake cannot drift.

## Integration

Two to four daemons, temp `$BEAM_DIR`s, dev DERP, fake worker, test authenticator. The
daemons run in the test process (`control.Run`, built with the `beamtest` tag) and the CLI
is called as `cli.Main`; where a scenario needs a dialer that misbehaves or falls silent, a
bare member node (transport only) stands in for a daemon:

| Scenario | Proves |
|---|---|
| init, join | admission on first contact in both directions; both pinned |
| third joins while second is offline; second returns | second admits third directly and also learns it via sync; one row |
| second joins while worker withholds first's revocation of third | second admits third until the tombstone arrives by sync, then terminates; documents eventual revocation |
| revoke while all tunnels are up | peers refuse within the sync delta, no reconnect needed |
| revoke with worker down | local effect immediate; `published: "pending"`; append lands after worker returns |
| leaked address, no entry | handshake completes, hello absent, closed within 5 s; 16-slot budget enforced |
| possession | valid entry, wrong MAC → `possession`; replay of hello from another tunnel refused |
| wrong passkey | assertion from a second credential → `wrong-passkey` at join; a revoke signed by an unrelated credential is refused |
| supersession | re-join with a new address; peers switch; older entry ignored |
| exec, pty | argv, stdin, stdout/stderr, exit, `cwd`, injected env, resize, kill on loss |
| grants | `msg` refuses `pty` on a live tunnel; `none` still syncs |
| mailbox | stored/delivered/rejected; crash between store and ack → duplicate suppressed; subscriber defer/ack; `send` while connected flushes immediately |
| junk in the log | valid assertion, unrelated blob → ignored |
| daemon lock | second daemon exits 1; connect-or-spawn loser connects to winner |
| reset | tunnels closed, fleet state gone, key kept, re-join works |

## n10 e2e

`desktop-e2e` runs a real second daemon with a seeded `$BEAM_DIR`, dev DERP and fake
worker; the app spawns its own daemon through the helper. Screenshots stay stable because
the seeded keys never change.

## Manual, per release

Real ceremonies on macOS Safari, Chrome on Linux, and phone-via-QR, each with `init` on a
fresh dir and `join` on a second; check that the derived directory key matches across
create/get and local/hybrid. Real NAT traversal between two networks; `beam peers` shows
`direct` or `relay`.
