# 10 · Testing

Everything is testable without a network, a browser, a passkey or Cloudflare. Four
pieces make that true: an in-process DERP, a software authenticator behind a build tag,
an in-process fake of the worker API, and daemons that take their `$BEAM_DIR` and
endpoints from flags.

## Unit tests

Plain `go test ./...`, one `_test.go` beside each file. Table tests for everything with
a table in this specification: peerId derivation, label and topic validation, scopes,
open-header refusal reasons, frame encoding, ack reasons, send outcomes.

**Entry vectors.** `internal/identity/testdata/` holds fixed PRF outputs, the keys they
derive to, fixed node keys, and signed entries and revocations: one valid each, one with
a mismatched `nodePublic`, one whose address embeds a different key, one signed under a
different `MPK`, one for a revoked id. Every verifier check has a vector that fails only
that check. Derivation is checked against the vectors so a change to salts or HKDF info
strings cannot go unnoticed.

**Frames and headers** get property tests (`pgregory.net/rapid`): encode/decode
round-trip, arbitrary byte splits across reads, header lines at and over the cap. Any
counterexample found becomes a fixed regression case.

**Mailbox** property tests: sequential delivery under random crash points,
duplicate suppression, quarantine of the unencodable, queue bounds.

## The dev DERP

tailcat's `cmd/tailcat` contains `runDevDERP` (~40 lines): an in-process
`derpserver` behind an `httptest` TLS server plus a loopback STUN responder, returned as
a `tailcfg.DERPRegion` with `InsecureForTests`. `internal/devderp` copies it. Tests set
`IN_TS_TEST=true`, which tailcat's own tests use to keep portmapper and captive-portal
probes off the real network.

`beam daemon --dev-derp` is a hidden flag that starts one and uses it as the only
region. Two daemons in two temp `$BEAM_DIR`s with `--dev-derp` pointed at the same
instance connect over loopback with a real WireGuard handshake, real disco and a real
relay fallback (force it by blocking UDP between them in the test).

## The software authenticator

The `beamtest` build tag compiles in `internal/ceremony/software.go`: when
`BEAM_TEST_AUTHENTICATOR=<path to a JSON file holding a credential id and a 32-byte PRF
secret>` is set, ceremony ops skip the browser, compute the two PRF outputs as
`HMAC-SHA256(secret, salt)` exactly as an authenticator would, and deliver them to the
loopback listener themselves. Derivation and signing are not bypassed. Release builds do
not include the tag.

## The fake worker

`internal/fakeworker` implements the two routes and the HMAC check in-process with an
in-memory map, and is started by tests on a loopback port passed to daemons as
`--directory-url`. A contract test runs the same Go client against the real worker under
miniflare in CI, so the fake cannot drift.

## Integration tests (Go)

`internal/e2e/` runs full flows with two or three daemons, dev DERP, software
authenticator, `t.TempDir()` for each `$BEAM_DIR`:

| Test | Proves |
|---|---|
| init → join | both machines hold `fleet.json`; the second is admitted by the first on its dial, pinned, dialed back |
| join a third while the second is offline, then bring the second back | the second admits the third on first contact, and also learns of it via `sync` from the first; both paths produce one record |
| worker down after join | existing machines keep connecting; a `join` fails with `directory-unavailable`; a `revoke` queues in `pending/` and appends at next start |
| exec round trip | argv, stdin, stdout, stderr, exit code, `cwd` `~/` expansion, injected env |
| pty round trip | resize, exit, process-group kill on close |
| scopes | `grant msg` refuses `pty` on a live tunnel |
| revoke | tunnel terminated, streams gone, the third machine refuses the revoked key at admission after `sync`; a `join` by the revoked id is refused |
| junk in the log | an entry appended with a wrong signature is fetched, fails verification, is ignored, and the daemon proceeds |
| mailbox across restart | queued while offline, delivered on reconnect, subscriber ack semantics |
| relay path | UDP blocked → `path: relay:dev`, streams still work |
| wrong root | a daemon with PRF secrets from a different credential derives a different `MPK`, cannot verify any entry, and reports `wrong-fleet` |
| leaked address | a client with a valid address but no entry completes the handshake and is closed at admission within 5 s |
| daemon lock | second daemon on the same dir exits 1 |

Each test that adds an assertion first breaks the behaviour and confirms the test
fails, then restores it.

## n10's e2e suites

n10's `desktop-e2e` runs a real second machine rather than a fake at the IPC layer, so
the feature is proved and not the mock. The fixture:

1. Builds or downloads the beam binary for the host platform.
2. Creates two `$BEAM_DIR`s seeded with fixed `key.json`, `fleet.json`, `peers.json`
   (pre-attested with the test passkey key, so `peerId`s and fingerprints in screenshots
   never change).
3. Starts a dev DERP, then `beam daemon --dev-derp … ` for the "remote" machine.
4. Launches the app with `BEAM_CONFIG_DIR` pointing at the local dir; the app spawns its
   own daemon through the bridge as in production.

The join flow is exercised end to end with the software authenticator and the fake
worker, so the browser hand-off is the one thing the e2e does not click through. `cli-e2e` gains no beam
coverage; the TUI does not expose beam directly.

## What is not tested automatically

- Real NAT traversal on the public internet. Manual QA: two machines on different
  networks, `beam peers` shows `direct` or `relay:<region>`.
- A real passkey ceremony with PRF in a real browser. Manual QA once per release on
  macOS Safari, Chrome on Linux, Edge on Windows, and phone-via-QR: `beam init` on a
  fresh dir, then `beam join` on a second.
- Windows ConPTY and the named pipe, until a Windows CI runner is added.
