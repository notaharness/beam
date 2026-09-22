# 10 · Testing

Everything is testable without a network, a browser or a passkey. Three pieces make that
true: an in-process DERP, a software authenticator behind a build tag, and daemons that
take their `$BEAM_DIR` and listen address from flags.

## Unit tests

Plain `go test ./...`, one `_test.go` beside each file. Table tests for everything with
a table in this specification: peerId derivation, label and topic validation, scopes,
open-header refusal reasons, frame encoding, ack reasons, send outcomes.

**Attestation vectors.** `internal/identity/testdata/` holds fixed node keys, a fixed
ES256 passkey keypair, and pre-computed assertions: one valid, one with the wrong
challenge, one with the wrong origin, one from a different credential, one with UV
unset. Every verifier check has a vector that fails only that check.

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
`BEAM_TEST_AUTHENTICATOR=<path to an ES256 private key PEM>` is set, `init.start` and
`add.confirm` skip the browser, produce a WebAuthn-shaped registration or assertion
locally (correct `clientDataJSON` with origin `https://pair.n10.is`, correct
`authenticatorData` with `rpIdHash` and UV set), and deliver it to the loopback listener
themselves. The verifier is not bypassed; it verifies a real assertion signed by a key
under test control. Release builds do not include the tag, so the environment variable
does nothing there.

## Integration tests (Go)

`internal/e2e/` runs full flows with two or three daemons, dev DERP, software
authenticator, `t.TempDir()` for each `$BEAM_DIR`:

| Test | Proves |
|---|---|
| init → join → add | `fleet.json`, pins and allowlists on both sides; the join address is spent |
| add a third, then reconnect the second | gossip: the second learns the third from the first without any server |
| exec round trip | argv, stdin, stdout, stderr, exit code, `cwd` `~/` expansion, injected env |
| pty round trip | resize, exit, process-group kill on close |
| scopes | `grant msg` refuses `pty` on a live tunnel |
| revoke | tunnel terminated, streams gone, propagated to the third machine, re-add refused |
| mailbox across restart | queued while offline, delivered on reconnect, subscriber ack semantics |
| relay path | UDP blocked → `path: relay:dev`, streams still work |
| wrong page | assertion over a different challenge is refused with `bad-assertion` |
| wrong root | second daemon given a different passkey key refuses the fleet and emits `trust-root-mismatch` |
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

The add flow is exercised end to end with the software authenticator, so the browser
hand-off is the one thing the e2e does not click through. `cli-e2e` gains no beam
coverage; the TUI does not expose beam directly.

## What is not tested automatically

- Real NAT traversal on the public internet. Manual QA: two machines on different
  networks, `beam peers` shows `direct` or `relay:<region>`.
- A real passkey ceremony in a real browser. Manual QA once per release on macOS Safari,
  Chrome on Linux, Edge on Windows: `beam init` on a fresh dir.
- Windows ConPTY and the named pipe, until a Windows CI runner is added.
