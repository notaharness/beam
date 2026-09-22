# Contributing

The spec in `docs/` is the contract. Code follows it; where they drift, one of them is
fixed in the same PR, and the PR says which. A choice the spec is silent on is recorded
in `docs/11-decisions.md`.

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
