# 09 · Build and distribution

## Repository layout

```
beam/
  go.mod                    module github.com/notaharness/beam; go 1.27
  cmd/beam/                 main: subcommand dispatch
  internal/
    identity/               peerId, fleet key derivation, entry signing and verification, fleet.json
    peers/                  peer table, revocations, scopes
    transport/              tailcat Server/Client wiring, addresses, admission, status
    stream/                 open header, frames, pty, exec, msg, sync handlers
    mailbox/                queues, seq, seen, flusher, quarantine
    control/                the local socket: ops, events, attach pump, lock
    ceremony/               loopback listener, URL building, PRF result parsing, key derivation
    directory/              worker client: fetch, append, entry encryption, request signing
    cli/                    subcommand implementations (thin socket clients)
    devderp/                in-process DERP + STUN for tests
    fakeworker/             in-process implementation of the worker API for tests
  worker/                   the Cloudflare Worker: directory API + the static ceremony page
    src/index.ts            routes, HMAC auth, D1 access; no crypto beyond HMAC verification
    public/index.html       the ceremony page
    schema.sql              one table
    test/                   vitest against miniflare
  npm/                      package.json templates for the shim and platform packages
  docs/                     this specification
```

`internal/` keeps the API private; the only public surface is the binary and the socket
protocol. No package imports `cmd/`. `transport` is the only package that imports
tailcat; `directory` is the only one that speaks HTTP.

## Build

```
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -tags "$(tr '\n' ',' < build-tags.txt)" \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o dist/beam-$GOOS-$GOARCH ./cmd/beam
```

`build-tags.txt` is copied from tailcat's repository and drops components beam never
uses (about 16% of the binary). No cgo on any target; tailcat's own release
configuration confirms this.

| Target | Notes |
|---|---|
| `darwin/arm64` | Go's linker ad-hoc signs it. No notarisation while distributed through npm (same status as node-pty's prebuilt `.node` files). |
| `darwin/amd64` | |
| `linux/amd64` | |
| `linux/arm64` | |
| `windows/amd64` | named pipe for the control socket; ConPTY for `pty` |

Measured on linux/amd64 with Go 1.27.1: 15–16.4 MB per target stripped; 21.5 s cold
build with a warm module cache; 0.14 s warm rebuild; 479 MB module cache. One Linux CI
runner cross-compiles all five.

## Versioning

beam has its own semver, independent of n10's. `main.version` is set from the git tag.
The socket protocol carries `v: 1` in every open header and `version` in `status`; a
client that needs a newer daemon says so with the two versions in the message. n10 pins
`@notaharness/beam` with a caret range and reads `status.version` at startup.

## npm packages

The pattern esbuild and node-datachannel use: one shim, N platform packages as
`optionalDependencies`.

| Package | Contents |
|---|---|
| `@notaharness/beam` | `bin/beam.js`: resolves the platform package, `execFileSync`s the binary with the same argv, propagates the exit code. `index.js` exports `binaryPath()` for the desktop bridge. |
| `@notaharness/beam-darwin-arm64` … `-win32-x64` | one file, the binary, `os` and `cpu` fields set so npm installs only the matching one |

`n10` and `n10-desktop` list `@notaharness/beam` as a dependency; the bridge calls
`binaryPath()`. A machine without Node installs from GitHub release assets or Homebrew
(`brew install notaharness/tap/beam`, later); the binary is the same file.

## The worker

One Cloudflare Worker at `pair.n10.is`, open source in `worker/`, doing two things: serving
the static ceremony page, and storing an append-only log of ciphertext per fleet. It has
no accounts, no sessions, no plaintext.

### Storage

D1, one table:

```sql
CREATE TABLE entries (
  fleet_id   TEXT    NOT NULL,   -- 64 hex, SHA-256(MPK)
  seq        INTEGER NOT NULL,   -- assigned by the worker, dense per fleet
  blob       BLOB    NOT NULL,   -- nonce ‖ ciphertext, ≤ 4096 bytes
  created_at INTEGER NOT NULL,
  PRIMARY KEY (fleet_id, seq)
);
```

Caps: 4 KiB per entry, 2,000 entries per fleet, 60 requests per minute per fleet. A
fleet with no requests for two years is deleted; members hold everything anyway.

### Authentication

Every request carries `Authorization: beam-hmac <fleetId>:<unix seconds>:<base64url mac>`
where `mac = HMAC-SHA256(K_auth, method ‖ "
" ‖ path ‖ "
" ‖ timestamp ‖ "
" ‖ SHA-256(body))`.
The worker stores `HMAC-SHA256(K_auth, "beam:verify")` per fleet on first append and
checks against it; it never holds `K_auth`. Timestamps outside ±5 minutes are refused.
`K_auth` is known to every fleet machine, so this proves "a member", not which one; a
stolen machine can read the log (it already holds the decrypted contents) and append
junk (readers discard it). That is the intended strength.

### Routes

| | |
|---|---|
| `GET /` | the ceremony page, with the CSP header below |
| `GET /v1/fleets/:fleetId/entries?since=<seq>` | `{ entries: [ { seq, blob } ] }`, ascending, at most 500; the client pages |
| `POST /v1/fleets/:fleetId/entries` | body is one blob; `201 { seq }`. Creates the fleet on first append. `413` over the cap, `429` over the rate. |

No `DELETE`, no `PUT`. Nothing reads a blob's contents; the worker could not if it tried.

### The ceremony page

`worker/public/index.html`, served with:

```
Content-Security-Policy: default-src 'none'; script-src 'sha256-<hash>'; style-src 'sha256-<hash>'
Referrer-Policy: no-referrer
X-Frame-Options: DENY
Cache-Control: public, max-age=300
```

CI recomputes the hashes and fails if the header is stale. Deploy is `wrangler deploy`
from `worker/` and needs Hermann's Cloudflare account; it is the only step in the repo
that touches Cloudflare. The page has no build step and no dependencies.

## Lint and format

`gofmt -l`, `go vet ./...`, `staticcheck ./...`. Complexity and length budgets as in n10
(`gocyclo` 12, files ≤ 300 lines excluding tests) enforced by `golangci-lint` with
exactly those two linters enabled beyond the defaults. Suppressions need a `//nolint`
with a reason.

## CI

On every push: `go vet`, `staticcheck`, `go test -race ./...`, cross-compile all five
targets, `worker/` vitest under miniflare, the Go directory client's contract test
against that same miniflare instance, verify the CSP hashes. On a tag: build, attach the five binaries to a GitHub
release, publish the six npm packages with the tag's version. The module cache is
restored from `actions/cache` keyed on `go.sum`.
