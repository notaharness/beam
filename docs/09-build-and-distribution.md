# 09 · Build and distribution

## Layout

```
beam/
  go.mod                    module github.com/notaharness/beam; go 1.27.1
  cmd/beam/                 main; subcommand dispatch
  internal/
    identity/               peerId, canonical JSON, entry/revocation build and verify, fleet.json
    transport/              tailcat Server/Client, addresses, admission (hello), possession MAC, liveness
    stream/                 header, frames, pty, exec, msg, sync handlers
    store/                  SQLite: peers, revocations, mailbox tables, pending appends
    mailbox/                flusher, receiver, subscriber fan-out over store
    directory/              worker HTTP client, blob encryption, append retry
    ceremony/               loopback listener, /cb landing page, URL building, PRF derivation
    control/                the socket: ops, events, attach pump, lock, connect-or-spawn
    cli/                    subcommands
    devderp/                in-process DERP + STUN for tests
    fakeworker/             in-process worker API for tests
  worker/                   Cloudflare Worker: directory API + ceremony page (TypeScript)
    src/index.ts            routes, WebAuthn assertion verification, D1
    public/index.html       the ceremony page
    schema.sql
    test/                   vitest under miniflare
  npm/                      shim and platform package templates
  docs/
```

`transport` is the only package importing tailcat; `directory` and `ceremony` are the
only ones speaking HTTP.

## Build

```
CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags "$(tr '\n' ',' < build-tags.txt)" \
  -ldflags "-s -w -X main.version=$VERSION" -o dist/beam-$GOOS-$GOARCH ./cmd/beam
```

`build-tags.txt` copied from tailcat. SQLite via `modernc.org/sqlite` (pure Go). Targets
in the first release: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`.
Windows follows when a Windows runner exists. Measured on linux/amd64: 15–16.4 MB per
binary stripped, 21.5 s cold build with warm module cache, 0.14 s warm rebuild.

## Versioning

beam has its own semver from the git tag. `status.version` on the socket is how clients
check; the peer stream header's `v` is the wire protocol version and is separate. n10
pins `@notaharness/beam` with a caret range.

## npm

| Package | Contents |
|---|---|
| `@notaharness/beam` | `bin/beam.js`: resolves the platform package and runs the binary with `child_process.spawn(bin, argv, { stdio: "inherit" })`, forwards `SIGINT`/`SIGTERM`/`SIGWINCH`, exits with the child's code or `128+signal`. `index.js` exports `binaryPath()`. |
| `@notaharness/beam-{darwin-arm64,darwin-x64,linux-x64,linux-arm64}` | one binary each; `os`/`cpu` set |

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
  read_hash      BLOB NOT NULL,      -- SHA-256(T_read)
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
```

Caps: 8 KiB per blob, 5,000 entries per fleet, 120 requests/min per fleet. No expiry.

### Routes

| | Auth | |
|---|---|---|
| `GET /` | none | ceremony page |
| `POST /v1/fleets` | body | `{ credentialId, credentialPublicKey, readToken, first: { statementHash, blob, assertion } }`. Verifies `first.assertion` under `credentialPublicKey` with challenge `SHA-256("beam-member:v1" ‖ statementHash)`, creates the fleet with `read_hash = SHA-256(readToken)`, stores the entry. `409` if the fleet exists. |
| `GET /v1/fleets/:id/entries?since=` | `Authorization: Bearer <T_read>` (compared by hash) | `{ credentialId, credentialPublicKey, entries: [ { seq, statementHash, blob, assertion } ], next? }`, ≤ 500 per page |
| `POST /v1/fleets/:id/entries` | the assertion in the body | `{ statementHash, blob, assertion }`. Verifies the assertion with the member or revoke domain (both are tried; the daemon says which via `kind`). `201 { seq }`; `200 { seq }` if `statementHash` already exists. |

Assertion verification (`@simplewebauthn/server`): origin `https://beam.n10.is`, RP ID
`beam.n10.is`, UV required, challenge as above, counter ignored. A revoked machine holds
`T_read` and can read; it cannot append.

### Ceremony page headers

```
Content-Security-Policy: default-src 'none'; script-src 'sha256-…'; style-src 'sha256-…'
Referrer-Policy: no-referrer
X-Frame-Options: DENY
Cache-Control: public, max-age=300
```

CI recomputes the hashes. Deploy is `wrangler deploy` from `worker/` with Hermann's
Cloudflare account; DNS for `beam.n10.is` is a Cloudflare-managed record pointing at the
worker.

## Lint

`gofmt`, `go vet`, `staticcheck`, `golangci-lint` with `gocyclo` 12 and a 300-line file
budget excluding tests. Suppressions carry a reason.

## CI

Push: vet, staticcheck, `go test -race ./...`, cross-compile four targets, worker vitest
under miniflare, the Go directory client's contract test against miniflare, CSP hash
check. Tag: build, GitHub release with binaries, publish the five npm packages.
