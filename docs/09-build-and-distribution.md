# 09 · Build and distribution

## Repository layout

```
beam/
  go.mod                    module github.com/notaharness/beam; go 1.27
  cmd/beam/                 main: subcommand dispatch
  internal/
    identity/               keys, peerId, attestation, verification, fleet.json
    peers/                  peer table, revocations, scopes
    transport/              tailcat Server/Client wiring, addresses, admission, status
    stream/                 open header, frames, pty, exec, msg, sync handlers
    mailbox/                queues, seq, seen, flusher, quarantine
    control/                the local socket: ops, events, attach pump, lock
    ceremony/               loopback listener, URL building, result parsing
    cli/                    subcommand implementations (thin socket clients)
    devderp/                in-process DERP + STUN for tests
  web/pair/                 the static ceremony page: index.html, _headers
  npm/                      package.json templates for the shim and platform packages
  docs/                     this specification
```

`internal/` keeps the API private; the only public surface is the binary and the socket
protocol. No package imports `cmd/`. `transport` is the only package that imports
tailcat.

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

## The ceremony page

`web/pair/index.html` deploys to Cloudflare Pages at `https://pair.n10.is`. It is one
file plus a `_headers` file:

```
/*
  Content-Security-Policy: default-src 'none'; script-src 'sha256-<hash>'; style-src 'sha256-<hash>'
  Referrer-Policy: no-referrer
  X-Frame-Options: DENY
  Cache-Control: public, max-age=300
```

CI recomputes the script and style hashes from the file and fails if `_headers` is
stale. Deploy is manual (`wrangler pages deploy web/pair`) and needs Hermann's
Cloudflare account; nothing else in the repo touches Cloudflare. The page has no build
step and no dependencies.

## Lint and format

`gofmt -l`, `go vet ./...`, `staticcheck ./...`. Complexity and length budgets as in n10
(`gocyclo` 12, files ≤ 300 lines excluding tests) enforced by `golangci-lint` with
exactly those two linters enabled beyond the defaults. Suppressions need a `//nolint`
with a reason.

## CI

On every push: `go vet`, `staticcheck`, `go test -race ./...`, cross-compile all five
targets, verify `_headers` hashes. On a tag: build, attach the five binaries to a GitHub
release, publish the six npm packages with the tag's version. The module cache is
restored from `actions/cache` keyed on `go.sum`.
