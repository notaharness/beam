# 09 · Build and distribution

## Layout

```
beam/
  go.mod                    module github.com/notaharness/beam; go 1.27.1
  build-tags.txt            tailcat's release build tags
  Makefile                  dist, test, crap, lint
  cmd/beam/                 main; subcommand dispatch
  internal/
    identity/               peerId, canonical JSON, entry/revocation build and verify, fleet.json
    transport/              tailcat Server/Client, addresses, admission (hello), possession MAC, liveness
    stream/                 header, frames, pty and exec processes, msg framing
    store/                  SQLite: peers, revocations, mailbox tables, pending appends
    mailbox/                flusher, receiver, subscriber fan-out over store
    directory/              worker HTTP client, blob encryption, append retry
    ceremony/               key and slot, URL building, the slot wait, HPKE open, PRF derivation
    control/                the daemon: dial loop, sync, stream dispatch; the socket: ops,
                            events, attach pump, lock, connect-or-spawn
    cli/                    subcommands; the QR code
    devderp/                in-process DERP + STUN for tests
    fakeworker/             in-process worker API for tests
  worker/                   Cloudflare Worker: directory API + ceremony page (TypeScript)
    src/index.ts            routes, WebAuthn assertion verification, D1
    public/index.html       the ceremony page
    schema.sql
    test/                   vitest under miniflare
  npm/                      shim and platform package templates
  tools/crap/               the CRAP gate over the coverage profile
  docs/
```

`transport` is the only package importing tailcat. The QR code is drawn from
`github.com/boombuler/barcode`'s encoder (no dependencies of its own); the braille
rendering is beam's. In the daemon's own code `directory` and `ceremony` are the only
ones speaking HTTP; `transport.HomeKey` fetches the DERP map through tailcat, and
`fakeworker` serves HTTP in tests.

## Build

```
CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags "$(cat build-tags.txt)" \
  -ldflags "-s -w -X main.version=$VERSION" -o dist/beam-$GOOS-$GOARCH ./cmd/beam
```

`make dist` runs this for each target, and again with `beamtest` added to the tags into
`dist/beamtest-$GOOS-$GOARCH`, the test kit ([10](10-testing.md)). `build-tags.txt` is
copied from tailcat: one comma-separated line; tests and lint use the same tags. SQLite
via `modernc.org/sqlite` (pure Go). Targets in the first release: `darwin/arm64`,
`darwin/amd64`, `linux/amd64`, `linux/arm64`. Windows follows when a Windows runner
exists. 21.2–22.5 MB per binary stripped ([03](03-transport.md), Footprint).

## Versioning

beam has its own semver from the git tag: tag `vX.Y.Z` releases `X.Y.Z`, which
`beam version`, `status.version` and the five npm packages carry without the `v`. The
version is canonical semver without build metadata (`npm/version.mjs`): no leading
zeros, no empty prerelease identifier, no `+build`, the spellings npm would publish
differently or not at all. One with a prerelease (`v1.2.3-rc.1`) is a prerelease.
`status.version` on the socket is how clients check; the peer stream header's `v` is the wire protocol version and is separate. n10
pins `@notaharness/beam` with a caret range.

## npm

| Package | Contents |
|---|---|
| `@notaharness/beam` | `bin/beam.js`: resolves the platform package and runs the binary with `child_process.spawn(bin, argv, { stdio: "inherit" })`, forwards `SIGINT`/`SIGTERM`/`SIGWINCH`, handling them from before the binary starts, exits with the child's code or `128+signal`. `index.js` exports `binaryPath()`. The platform packages are its `optionalDependencies`, the one list of platforms. |
| `@notaharness/beam-{darwin-arm64,darwin-x64,linux-x64,linux-arm64}` | one binary each, named `beam`; `os`/`cpu` set |

`npm/beam/` is the shim and `npm/platform/package.json` the platform packages' template.
`node npm/pack.mjs X.Y.Z` writes all five into `dist/npm/` from the binaries `make dist`
left in `dist/`, each ready for `npm publish` as it is. Every package is MIT and carries
the repository's `LICENSE` and a README: the shim's says what beam is, the platform
packages' to install the shim instead.

## The worker

At `https://beam.n10.is`, open source in `worker/`. It serves the ceremony page and
stores the directory. It verifies WebAuthn assertions on writes and reads by bearer
token. It never sees a plaintext entry.

### Storage (D1)

```sql
CREATE TABLE fleets (
  fleet_id       TEXT PRIMARY KEY,   -- 64 hex, SHA-256(credential public key)
  credential_id  TEXT NOT NULL,
  credential_pk  BLOB NOT NULL,      -- COSE
  read_hash      BLOB NOT NULL UNIQUE, -- SHA-256(T_read)
  created_at     INTEGER NOT NULL
);
CREATE TABLE entries (
  fleet_id       TEXT NOT NULL REFERENCES fleets,
  seq            INTEGER NOT NULL,   -- dense per fleet
  statement_hash BLOB NOT NULL,
  blob           BLOB NOT NULL,      -- ≤ 8192 bytes
  assertion      TEXT NOT NULL,      -- JSON
  created_at     INTEGER NOT NULL,
  PRIMARY KEY (fleet_id, seq),
  UNIQUE (fleet_id, statement_hash)  -- appends are idempotent
);
CREATE TABLE slots (
  slot_id        TEXT PRIMARY KEY,   -- 22 base64url, SHA-256(readKey)[0:16]
  sealed         BLOB,               -- ≤ 8192 bytes; NULL once read
  created_at     INTEGER NOT NULL    -- the write; the row lives five minutes from it
);
CREATE INDEX slots_created ON slots (created_at);
```

Caps: 16 KiB per request body, counted as it arrives whatever `Content-Length` says; 8
KiB per blob; 5,000 entries per fleet, checked by the statement that allocates the seq;
120 requests/min per fleet. No expiry for fleets and entries. A slot holds at most 8 KiB
of sealed result and lives five minutes from its write, the ceremony's timeout: a row
older than that is absent to every route, and each write deletes the expired ones. Slot
reads and slot writes are each limited to 120 requests/min per client address
(`CF-Connecting-IP`), through the same binding, counted apart so that reads cannot spend
the page's one write; a waiting daemon makes about three reads a minute. A waiting read
only reads the database; the one that takes the result writes once. The
client holds the worker to the same bounds: a response at most a full page of the
largest entries, at most 500 entries a page and 5,000 in all, a `next` only after a full
page and past `since`, a minute for a whole read. A worker outside them is unavailable.

### Routes

| | Auth | |
|---|---|---|
| `GET /` | none | ceremony page |
| `POST /v1/fleets` | body | `{ credentialId, credentialPublicKey, readToken, first: { statementHash, blob, assertion } }`. Verifies `first.assertion` under `credentialPublicKey` with challenge `SHA-256("beam-member:v1" ‖ statementHash)`, creates the fleet with `read_hash = SHA-256(readToken)`, stores the entry. `409` if the fleet exists. |
| `GET /v1/entries?since=` | `Authorization: Bearer <T_read>`; the fleet is the one whose `read_hash` is `SHA-256(T_read)` | `{ fleetId, credentialId, credentialPublicKey, entries: [ { seq, statementHash, blob, assertion } ], next? }`, ≤ 500 per page, `seq > since`. Every token that opens no fleet, malformed or unknown, gets the same `401`. |
| `POST /v1/slots/:slot` | none: whoever has the ceremony URL | `{ sealed }`, the page's HPKE ciphertext, unpadded base64url, at most 8 KiB decoded. `201 {}`. `409` if the slot was written in the last five minutes, read or not: one write per slot. |
| `GET /v1/slots/:slot` | `Authorization: Bearer <readKey>`, which must hash to `:slot` | Holds the request up to 25 s for the write. `200 { sealed }`, and the ciphertext is deleted: one read. `204` if nothing arrived; the daemon asks again. `410` if it was read already. Every key that does not hash to the slot gets the same `401`. |
| `POST /v1/fleets/:id/entries` | the assertion in the body | `{ kind, statementHash, blob, assertion }`. Verifies the assertion with the domain of `kind` (`member` or `revoke`). `201 { seq }`; `200 { seq }` if `statementHash` already exists; `404` for an unknown fleet. |

Assertion verification (`@simplewebauthn/server`): origin `https://beam.n10.is`, RP ID
`beam.n10.is`, UV required, challenge as above, counter ignored, and the assertion's
credential id must be the fleet's. A refused append is `403`, a body or a blob over its
cap or a full fleet `413`, and a fleet over its rate `429`, through Workers' rate-limit
binding. A revoked machine holds `T_read` and can read; it cannot append. A slot is
`:slot` as 22 base64url characters encoding 16 bytes; any other is `404`. The worker can
open no slot's ciphertext; it never sees the key.

### Ceremony page headers

```
Content-Security-Policy: default-src 'none'; script-src 'sha256-…'; style-src 'sha256-…'; connect-src https://beam.n10.is/v1/slots/
Referrer-Policy: no-referrer
X-Frame-Options: DENY
Cache-Control: public, max-age=300
```

The page is a static asset (`worker/public/`) and these headers live in its `_headers`;
a worker test recomputes the hashes, and the page's flows run in Chromium under them,
failing on any policy violation. `connect-src` names the slot route's path prefix,
not `'self'`: the page can write a slot and reach nothing else. A deploy that adds a
table runs `schema.sql` against the remote database first (`worker/README.md`). Deploy is `wrangler deploy` from `worker/` with
Hermann's Cloudflare account, `beam.n10.is` a custom domain of the worker;
`worker/README.md` has the commands.

## Lint

`make lint`: `golangci-lint` (pinned in the `Makefile`), which runs `gofmt`, `go vet` and
`staticcheck` among its defaults, plus `gocyclo` at 12 and `revive`'s `file-length-limit`
at 300 lines, both excluding tests. Suppressions carry a reason.

`make crap`: runs `make test`, which writes `cover.out` over the whole module
(`-coverpkg=./...`), then scores every non-test function that build compiled (not, say,
another OS's) with Savoia's CRAP metric,
`complexity² × (1 − coverage)³ + complexity`, complexity counted as `gocyclo` counts it.
Any function above 30 (crap4j's threshold) fails the build. A complexity-12 function
therefore needs at least half its statements covered; an untested one must stay at
complexity 4 or below.

## CI

Push (`.github/workflows/ci.yml`): job `ci`, required on `main`: the worker's vitest in
workerd (the CSP hashes included) and the ceremony page's flows in Chromium, `make
lint`, `make crap` (`go test -race` with coverage, the Playwright ceremony test among
them, then the CRAP gate), `make dist`
(cross-compile four targets), and the `directory` and `ceremony` packages' tests again
against the worker under `wrangler dev`, the Go directory client and the slot reader
held to the real routes. Job `darwin`: `make test` on macOS, where the process
lifetimes (kqueue, not waitid) and the in-process daemons run for real. The `ci` job also
runs the npm packages' node tests.

Tag (`.github/workflows/release.yml`, on `v*`), two jobs. `release`: `npm/version.mjs`
reads the tag, and the job stops at one that is not canonical. Then `make dist`,
`npm/pack.mjs`, and a check that `npm publish --dry-run` would publish every package at
the version the binary prints; only then a GitHub release with the four binaries, the
four `beamtest-*` binaries of the test kit and `SHA256SUMS` over all eight, marked a
prerelease for a prerelease tag. The test kit goes to the GitHub release alone, never to
npm. `publish`, its own job so that it can be re-run alone:
packs the same binaries, handed over as a workflow artifact, and publishes the platform
packages and last the shim, with provenance, under `next` for a prerelease. It
authenticates through npm's trusted publishing (OIDC: `id-token: write`, npm 11.5.1 or
later, which Node 24 has), each package naming `notaharness/beam` and `release.yml` as
its trusted publisher. A package with none yet, at the first release, is published with
the `NPM_TOKEN` secret, which npm falls back to while it is set. Run by hand from `main`
(`workflow_dispatch`) with a version, the workflow rehearses that release: no GitHub
release, whose the token is if there is one, and `npm publish --dry-run`. CONTRIBUTING.md
has the steps.
