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
the work, and docs/09 describes it.

1. Every PR of the stack is merged and `main` is green.
2. Rehearse from `main`: `gh workflow run release.yml -f version=vX.Y.Z`, then
   `gh run watch`. The whole workflow runs without a GitHub release and with
   `npm publish --dry-run`.
3. `git tag vX.Y.Z main && git push origin vX.Y.Z`. Job `release` checks the tag is
   canonical semver, builds the four binaries and the four of the test kit, packs the five
   npm packages, checks npm would publish each at the binary's version, and creates the
   GitHub release with all eight and `SHA256SUMS`. Job
   `publish` publishes the four platform packages, then `@notaharness/beam`, with
   provenance.
4. Check: `gh release view vX.Y.Z`; `npm view @notaharness/beam version`; and on a
   machine without beam, `npx @notaharness/beam@X.Y.Z version` prints `X.Y.Z`.

A tag with a prerelease, `v0.2.0-rc.1`, makes a GitHub prerelease and publishes under
npm's `next` dist-tag, so `latest` stays on the last release:
`npm view @notaharness/beam dist-tags`, `npx @notaharness/beam@next version`.

When a job fails part-way:

- `release` failed: nothing is public but the tag. Fix the cause, delete the tag
  (`git push origin :refs/tags/vX.Y.Z && git tag -d vX.Y.Z`) and tag again.
- The GitHub release exists and `publish` failed before publishing anything: fix the
  cause (npm authentication, say) and re-run the failed job,
  `gh run rerun <run-id> --failed`. A re-run runs the workflow as it was at the tag, so a
  fix to `release.yml` itself needs the next version.
- `publish` published some packages and not others: npm never takes a version twice, so
  the version is spent. Delete its GitHub release (`gh release delete vX.Y.Z`) and
  release the next patch.

### npm trusted publishing

The workflow holds no npm token. Each of `@notaharness/beam`,
`@notaharness/beam-darwin-arm64`, `@notaharness/beam-darwin-x64`,
`@notaharness/beam-linux-x64` and `@notaharness/beam-linux-arm64` has a trusted
publisher on npmjs.com (the package's Settings, Trusted Publisher, GitHub Actions):
organization `notaharness`, repository `beam`, workflow filename `release.yml`, no
environment. Each package's publishing access requires two-factor authentication and
disallows tokens. npm sets a trusted publisher only on a package that exists, so a new platform
package is published once by hand before a release can publish it.
