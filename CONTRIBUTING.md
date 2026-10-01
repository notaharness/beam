# Contributing

The spec in `docs/` is the contract. Code follows it; where they drift, one of them is
fixed in the same PR, and the PR says which. A choice the spec is silent on is recorded
in `docs/11-decisions.md`.

A security vulnerability is reported privately, as [SECURITY.md](SECURITY.md) says, not
in an issue or a pull request.

## No clutter

One way to do each thing. No flag nobody sets, no abstraction with one implementation,
no code kept "in case", no fallback behind a switch. When something is replaced, the old
thing is deleted in the same change. Delete rather than layer.

## Milestones

Work lands in milestone branches (`impl/m1-…`, `impl/m2-…`) published as one GitHub
stack with `gh stack` (github/gh-stack): each milestone's PR is based on the previous
milestone's branch, so work never waits on a merge. No standalone PRs against `main`.
The owner reviews and merges the stack bottom-up.

## Checks

`make lint`, `make crap` and `make dist` are what CI runs on every push, and a PR cannot
merge until they pass. Lint budgets: cyclomatic complexity 12 per function and 300
lines per file, tests excluded. A `//nolint` carries its reason. `make crap` fails any
function whose CRAP score (`complexity² × (1 − coverage)³ + complexity`) exceeds 30.

Test first, and prefer the failing integration scenario (the `docs/10` list: real daemons
on the dev DERP and the fake worker) over unit tests; keep table and property tests for
boundaries such as codecs and canonical JSON. No tautological tests: a test that
restates the implementation, asserts a constant, or passes with the behaviour broken is
deleted. Every table in the spec has a table test. When you add a test, break the
behaviour it covers, watch it fail, then restore it.

## Commits

Conventional Commits with a scope: `feat(transport):`, `test(identity):`,
`chore(ci):`, `docs(spec):`. Small commits. Dependency additions are committed apart
from the code that uses them.

## Releasing

Hermann tags; nothing else releases or publishes. `.github/workflows/release.yml` does
the work, and docs/09 describes it. A release is a bump, a tag and a push of the tag:

1. Every PR of the stack is merged and `main` is green.
2. Bump: pick the version after the last tag (`git describe --tags --abbrev=0 main`),
   `vX.Y.Z-beta.N` for a beta or `vX.Y.Z` for a release. The tag is the version;
   nothing in the repository carries it.
3. `git tag vX.Y.Z main && git push origin vX.Y.Z`. Job `release` checks the tag is
   canonical semver, builds the four binaries and the four of the test kit, packs the five
   npm packages, checks npm would publish each at the binary's version, and creates the
   GitHub release with all eight and `SHA256SUMS`. Job `publish`, in the `npm`
   environment, checks that npm takes the workflow as every package's trusted publisher
   (and lets it move `latest`, for a beta while `LATEST_FOLLOWS_BETA` is on), then
   publishes the four platform packages and `@notaharness/beam`, with provenance.
4. Check: `gh release view vX.Y.Z`; `npm view @notaharness/beam dist-tags`; and on a
   machine without beam, `npx @notaharness/beam@X.Y.Z version` prints `X.Y.Z`.

A tag with a prerelease, `v0.2.0-beta.1`, makes a GitHub prerelease and publishes every
package under npm's `beta` dist-tag; a tag without one publishes under `latest`. While
beam is beta-only, `LATEST_FOLLOWS_BETA: true` at the top of `release.yml` also moves
every package's `latest` to the beta, the platform packages' first and the shim's last,
so `npm install -g @notaharness/beam` gets it. At 1.0, set it to `false`: from then on
`@notaharness/beam` is the last release and `@notaharness/beam@beta` the last beta. The
shim depends on each platform package at its own exact version, so either tag installs
the platform packages of the same release. If `publish` fails after the publish and
before `latest` has moved, move it by hand (`npm dist-tag add
@notaharness/<package>@X.Y.Z latest`, platform packages first).

After a change to `release.yml` or to a trusted publisher, rehearse from `main` before
the next tag, with the next, unpublished version (npm's dry run refuses one it has):
`gh workflow run release.yml -f version=vX.Y.Z`, then `gh run watch`. The whole
workflow runs without a GitHub release or an npm publish, and checks that npm takes it
as every package's trusted publisher. For a beta while `LATEST_FOLLOWS_BETA` is on, it
also adds and removes the throwaway dist-tag `release-check` on every package.

When a job fails part-way:

- `release` failed: nothing is public but the tag. Fix the cause, delete the tag
  (`git push origin :refs/tags/vX.Y.Z && git tag -d vX.Y.Z`) and tag again.
- The GitHub release exists and `publish` failed before publishing anything: fix the
  cause (npm authentication, say) and re-run the failed job,
  `gh run rerun <run-id> --failed`. A re-run runs the workflow as it was at the tag, so a
  fix to `release.yml` itself needs the next version.
- `publish` published some packages and not others: npm never takes a version twice, so
  the version is spent. Delete its GitHub release (`gh release delete vX.Y.Z`) and
  release the next patch, or the next `beta.N`.

### npm trusted publishing

The workflow holds no npm token. Each of `@notaharness/beam`,
`@notaharness/beam-darwin-arm64`, `@notaharness/beam-darwin-x64`,
`@notaharness/beam-linux-x64` and `@notaharness/beam-linux-arm64` has a trusted
publisher on npmjs.com (the package's Settings, Trusted Publisher, GitHub Actions):
organization `notaharness`, repository `beam`, workflow filename `release.yml`,
environment `npm`, with "Allow npm dist-tag" on. Each package's publishing access
requires two-factor authentication and disallows tokens. npm sets a trusted publisher
only on a package that exists, so a new platform package is first published by hand at
`0.0.0`, a version no release uses; then set its trusted publisher and publishing
access.

The `npm` environment takes deployments from `v*` tags and `main` alone, and the
repository's tag ruleset lets only an admin create, move or delete a `v*` tag. So npm
takes a publish only from `release.yml` run at a `v*` tag or on `main`, whose
`release.yml` publishes on a tag push alone: whoever can push a `v*` tag or change
`main` can publish.
