# beam, for agents

beam connects the machines one person owns: `pty`, `exec` and durable `msg` streams over
tailcat tunnels, with one passkey as the root of trust. A Go daemon and CLI
(`cmd/beam`, `internal/`), the Cloudflare worker at `beam.n10.is` (`worker/`) and the npm
packages (`npm/`).

- **The spec in `docs/` is the contract.** Code follows it; when they drift, fix one in
  the same PR and say which. A choice it is silent on is recorded in
  `docs/11-decisions.md`; a sentence that is wrong is raised, not silently diverged from.
- **No clutter.** One way to do each thing: no flag nobody sets, no abstraction with one
  implementation, no code kept "in case". Delete rather than layer. (CONTRIBUTING.md)
- **Test first, integration first.** The `docs/10` scenarios on in-process daemons, the
  dev DERP and the fake worker come before unit tests; every table in the spec has a
  table test. Break the behaviour, watch the test fail, restore it.
- **Checks.** `make lint` (gocyclo 12, 300-line files), `make test` (race, `beamtest`
  tag, coverage), `make crap` (test, then CRAP at most 30 per function), `make dist`.
  Worker: `cd worker && npm ci && npx vitest run`. npm: `node --test 'npm/**/*.test.js'`.
  Go's toolchain is the one `go.mod` names; build tags `$(cat build-tags.txt),beamtest`.
- **Flow.** Milestone branches published as one stack with `gh stack`, each PR on the
  previous branch; no standalone PRs against `main`. Conventional Commits with a scope,
  small commits. Hermann reviews and merges.
- **Releases.** CONTRIBUTING.md "Releasing". Never push a tag, create a release or
  publish to npm: Hermann tags.

## Test safety

This machine may run a real beam daemon, which other tools talk through, and real tmux
sessions.

- Never use the default beam directory or socket: tests and manual runs set
  `BEAM_CONFIG_DIR` to a temporary directory short enough for the socket path (103
  bytes on macOS), as the test harness does.
- Stop a daemon through its socket (`daemon.shutdown`) or by the PID you started. Never
  `pkill beam` or `pkill -f`: that kills the real daemon, or your own shell.
- Tests run on `internal/devderp`, isolated and relay-only from `TestMain`, and the fake
  worker. Never point a test at `beam.n10.is` or Tailscale's DERP servers.
- tmux in manual checks runs on its own server (`tmux -L beam-qa`), also inside
  `beam connect`; never attach to, send keys to or kill a session you did not create.
