# beam final review — 2026-09-23

Reviewed the whole tree at `9aeb888`, the tip of the stacked PRs #2, #3, #5, #6 and #7:
`README.md` and `docs/01` to `docs/11` first, then every non-test source file, then the
tests, then `make dist`, `make lint`, the full suite with `-race` and coverage, and three
daemons run by hand in this process against the dev DERP, the fake worker and the
`beamtest` authenticator (init, join ×2, exec, connect, msg, alias, grant, revoke, fleet
reset, re-join). Severity is **high** (wrong behaviour a user will hit, or a security
gap), **medium** (a promise the code does not keep, or a trap), **low** (drift, waste,
polish). Line numbers are at `9aeb888`.

**Verdict.** The code implements the specification faithfully in nearly every mechanism
I traced, the trust boundaries in `docs/01` hold from the tap to the shell, and the
integration suite is the real thing: real daemons, real tunnels, real processes, and the
races the spec cares about are pinned by hooks rather than sleeps. I found no path by
which the network, the DERP operator, the worker, or a revoked machine gains membership
or a shell. What stands between this tree and `v0.1.0` is narrower: one silent mail-loss
bug at the seam between `fleet reset` and the mailbox, where a re-joined machine reports
`delivered` for messages the recipient discarded as duplicates; a revoked machine that
never learns it was revoked and tells its user it will deliver mail it never will; a
ceremony that times out with the wrong token; and a handful of numbers, log lines and
test gaps listed below. The codebase is small for what it does (8,038 lines of Go
outside tests, 5,815 in tests, a 212-line worker) and carries little debris; the
simplicity findings are trims, not restructurings.

## 1. Does the code implement the spec?

Divergences by document. Where the code is right and the spec is stale, the fix is to the
spec.

### README, 01 · Model

- **low — "read at join and at daemon start" is read twice at join.**
  `internal/control/ceremonies.go:90` reads the directory in `readFleet`, then
  `enrollAs` → `enroll` starts `readDirectory` at `internal/control/daemon.go:161`,
  which reads it again and re-learns the same records. Two reads per join, against a
  120/min budget per fleet. Fix: `enroll` takes the records the caller already has, or
  `join` skips `readDirectory` by passing them through `enrollAs`.
- Everything else in 01 is implemented as written. The blast-radius statement matches
  the default grant and the injected `BEAM_DIR`.

### 02 · Identity

- **high — `fleet reset` restarts `send_seq` at 1, so a machine that resets and re-joins
  has its first messages silently dropped as duplicates while reporting `delivered`.**
  `internal/store/pending.go:60` deletes `send_seq` and `seen` with the peers;
  `internal/store/store.go:95` re-creates the counter at 1 when the peer is pinned again.
  The recipient kept `seen.high_seq` for this sender (the sender's `peerId` is unchanged
  because `key.json` is kept, as 02 says), so every envelope with `seq ≤ high_seq` is
  answered `duplicate`, and `mailbox.Settle` treats `duplicate` as delivered. Reproduced
  by hand: beta sent two messages to alpha, reset, re-joined, sent three; alpha holds only
  the third, and beta printed `delivered to alpha` three times. This contradicts
  `docs/05-mailbox.md:45` ("never a restart at 1") and the whole point of `stored`.
  Fix: `Reset` keeps `send_seq` (and may keep `seen`); a counter is per key, not per
  enrolment. Add the scenario to `TestFleetReset`: mail before reset, mail after, count
  on the recipient.
- **medium — `ceremony-timeout` is unreachable; a ceremony nobody taps ends
  `ceremony-cancelled`.** `internal/control/flow.go:33` starts the flow's context with
  `ceremony.Timeout`; `internal/ceremony/ceremony.go:107` starts a second five-minute
  timer only when `Wait` is called, later. The context always fires first, and `Wait`
  maps it to `ErrCancelled`. For `init` the second `get` gets whatever is left of the
  first's five minutes. `docs/06-control-socket.md:134` lists `ceremony-timeout` as a
  token; no test reaches it. Fix: one clock. Drop the timer in `Wait` and have the flow
  distinguish its own deadline (`context.DeadlineExceeded` → `ceremony-timeout`) from
  `ceremony.cancel`; or give each `another` its own timeout.
- **low — `$BEAM_DIR` listing omits `daemon.log`** (`docs/02-identity.md:57`), which
  `internal/cli/daemon.go:18` writes on `--detach`. Spec is stale.
- **low — 02 says a revocation is "checked after any revocations already received…have
  been applied" as step 3 of admission; the code checks it before possession.**
  `internal/control/serve.go:185` verifies membership and revocation together, before
  `internal/transport/admission.go:284` checks the MAC. Only the refusal token differs
  when both fail (`revoked` instead of `possession`), which leaks nothing a member does
  not already know. Update `docs/03-transport.md:63` to the code's order, or move the
  revocation check after the MAC; the code's order is simpler.
- **low — a second `beam revoke` of an already revoked peer costs a tap and appends a
  second record.** `internal/control/ceremonies.go:142` builds the statement without
  checking `p.Revoked`. Fix: answer `revoked-peer` from `revoke.start`.
- Verified as written: canonical JSON (RFC 8785 with the safe-integer restriction),
  challenge domains, entry verification steps 1–6 in `internal/identity/verify.go` with
  UV required and the sign count ignored, supersession, `fleetId`, HKDF derivations,
  `K_dir` consistency on join and revoke, the peer table, sync framing (200 records,
  1 MiB), the loopback ceremony with `Host` pinning, `state`, the `/cb` landing page and
  its CSP, and `fleet reset`'s file and table effects other than the counter above.

### 03 · Transport

- **low — footprint numbers are stale.** `docs/03-transport.md:126` and
  `docs/09-build-and-distribution.md:47` say 15–16.4 MB stripped; `make dist` at this
  commit produces 21.2–22.4 MB (SQLite and go-webauthn landed after the spike). Cold
  build here was 10.8 s wall for four targets. Refresh the table.
- **low — `connected` means more than 03 says.** `docs/03` defines `connected` as
  "hello succeeded on the tunnel this machine dialed"; `internal/control/sync.go:59`
  sets it only after `sync` is open and the full dump plus `end` has been written. That
  is the better definition (a peer is connected when it can be told things). Update the
  spec.
- Verified as written: one `Server` on the node key, a `Client` per peer with a fresh
  key, `AllowedClients` never set, `PeerEnv` failing closed, the 16-slot hello budget,
  5 s hello and header deadlines, tunnel retirement on sync end or 30 s silence, 15 s
  ping, 2 s → 5 min jittered backoff reset on contact, learned entry and `msg.send`,
  address change closing the old tunnel, `path` from `Server.Status`, `--derp-map`.

### 04 · Streams

- Implemented as written: header and response, frames, `hello`, `sync`, `pty` (login
  shell rules, resize bounds, SIGHUP then SIGKILL after 5 s), `exec` channels and
  `stdin-eof`, the 4-frame input window with `taken` and `window`, `msg` with one
  stream per tunnel, grants (unparseable → `none`), injected environment, and every
  limit in the table. `close` is followed by a half-close and a bounded wait
  (`internal/stream/process.go`, `finish`).
- **low — the `exec` group is killed with SIGKILL at once on opener loss**, which is
  what 04 says ("kills the process group"), but a reader could take "as for `pty`" to
  mean the HUP grace applies. One sentence in 04 would settle it.

### 05 · Mailbox

- The `send_seq` finding above is the one real divergence.
- **low — "A recipient's outbound bound counts its quarantine too"** (05, Bounds) reads
  as the recipient's; the code (`store.Enqueue`) counts the *sender's* quarantine against
  the sender's outbound bound, which is what makes sense. Fix the sentence.
- Verified as written: envelope limits (256 KiB decoded, 960 KiB serialized, topic
  0–128), the tables, every transaction in the table, the 2 s head retry, the 30 s
  untaken-write drop, permanent versus retried refusals, all three outcomes with both
  `pendingReason`s and the exact sentences, subscribers with one in-flight envelope,
  `defer` semantics (D33) and the 10 s unread disconnect.

### 06 · Control socket

- **low — the Go helper has no reconnect-with-backoff.** `docs/06-control-socket.md:24`
  gives the shared helper a 500 ms → 30 s reconnect; `internal/control/client.go:40`
  connects or spawns once. The CLI is one-shot and needs none, so the code is right; the
  spec should say the backoff belongs to the desktop's port (08).
- **low — an unenrolled daemon also answers `events.subscribe`**
  (`internal/control/ops.go:19`), which `docs/06-control-socket.md:13` does not list.
  The desktop needs it before enrolment; add it to the spec.
- **low — `status.ready` is always equal to `status.enrolled`.**
  `internal/control/ops.go:94` sets both together, because `enroll` publishes the
  enrolment only after `transport.Start` succeeded. The spec's "reports transport state"
  describes a state that never exists. Either drop `ready` or make it mean something
  (for instance, the Server's listener is up while the first dials run). Dropping is
  simpler.
- **low — `possession` is listed as a socket error** (`docs/06:134`) but no op can
  return it; it only appears in a hello response. Remove it from the list.
- Verified as written: the lock, socket mode, stale-socket removal, connect-or-spawn,
  `--detach`, shutdown semantics, NDJSON with unescaped markup, 1 MiB lines, the 10 s
  unread disconnect, attach framing and its window, every op's request and result shape,
  reservations expiring at 10 s, every close reason, every event, `busy`,
  `ceremony-state`, `published: "pending"` with `directory.published`, and
  `directory-unavailable` on join.

### 07 · CLI

- **low — "`--json` prints the socket result" is true of `status` and `peers` only.**
  `docs/07-cli.md:4` states it for the CLI at large; only two subcommands take it. Either
  is fine; say which.
- **low — "1 other machines known"** (`internal/cli/enrol.go:59`) with a count of one.
- **low — `beam init` says `created fleet` for a fleet that already existed.** A
  passkey that already has a fleet (reset then `init` on the same passkey) takes the
  `Append` branch at `internal/control/directory.go:47` and lands in the old fleet, then
  `readDirectory` learns the old members. The outcome is right; the sentence is not.
  Print `joined fleet` when the append did not register, or refuse `init` with a hint
  to `join`.
- Verified: every usage line, exit codes (0/1/2 and the remote status, 137 for
  SIGKILL), every stage name in order, the init/join/revoke/reset sentences, the
  headless `ssh -L` line, raw-mode `connect` with SIGWINCH, `msg send` sentences,
  `msg listen` acking after the write, `msg queue` paging.

### 08 · n10 integration

Out of this tree. Nothing to check beyond the socket contract, which holds.

### 09 · Build and distribution

- **low — binary size stale**, as under 03.
- **low — "`directory` and `ceremony` are the only ones speaking HTTP"**
  (`docs/09:35`): `fakeworker` (test-only) and `transport.HomeKey` (through tailcat's
  `FetchDERPMap`) also do. Say "in the daemon's own code" or list them.
- **low — D24 says "a hundred-line tool"**; `tools/crap` is 321 lines plus 256 of
  tests. Still a good trade; fix the number or drop it.
- Verified: layout matches the tree exactly, build line, `build-tags.txt`, four
  targets, versioning without `v`, the five npm packages with `optionalDependencies` as
  the one platform list, the D1 schema (identical to `schema.sql`), all caps, all three
  routes with their status codes, `_headers` (the vitest recomputes the hashes), lint
  budgets, the CRAP gate (max 14.2 at this commit), and both workflows.

### 10 · Testing

See section 4. The list of vectors matches the spec's list exactly; the property tests
exist; the integration table is covered with two gaps noted there.

### 11 · Decisions

Every decision in the register is implemented as stated. D25 (a stalled hello leaves
its key unbound), D26, D30, D31, D33–D39 and D41–D44 I checked against the code
specifically; all hold.

## 2. Whole-system security

The path from a tap to a byte in a remote shell, with what is trusted at each hop:

1. **Tap.** The page at `beam.n10.is` builds a `get` whose challenge is
   `SHA-256("beam-member:v1" ‖ statementHash)`; the daemon built the statement and the
   challenge (`internal/control/enrolment.go`, `signing`). The page is trusted for one
   operation, as 01 says; it cannot mint a second statement. The `/cb` result is not
   trusted: the daemon re-verifies the assertion under the credential it knows
   (`identity.Credential.Verify`) and, at `init`, verifies the attestation itself
   (`ceremony.Result.Credential`). A hostile page's `failed` or garbage is
   `bad-assertion`. State is 128 random bits, compared in constant time, `Host` pinned
   to `127.0.0.1:<port>`.
2. **Directory.** The worker verifies the assertion in the domain of `kind` under the
   fleet's credential (`worker/src/index.ts`, `verified`) and never sees plaintext. At
   join the daemon verifies its *own* fresh assertion under the credential the worker
   returned before believing anything else (`internal/control/ceremonies.go:79`,
   `signed`), so a worker that lies about the credential fails there. Every record
   read is opened under `K_dir` with AAD `fleetId ‖ statementHash`, its statement is
   re-hashed against the stored hash, and it is then verified like any other
   (`internal/directory/blob.go`, `Open`; `internal/control/sync.go:219`, `learn`).
   Junk costs a slot. Withholding is the accepted exposure.
3. **Tunnel.** WireGuard with the PSK from the address. The acceptor learns the
   client key from `PeerEnv` and fails closed without it. Nothing is trusted until
   hello: entry verified (including revocation) then possession by X25519 MAC over
   `C ‖ S ‖ R ‖ nonce` with `C` taken from the tunnel, not the frame
   (`internal/transport/admission.go:275`). Pin and dial-back happen inside the
   binding, before the dialer hears `ok` (`TestPinnedBeforeOK`), and nothing happens
   on a failed MAC (`TestPossessionBeforeSideEffects`).
4. **Stream.** Every inbound stream is classified by the tunnel's key: only a bound
   tunnel gets a header read; an unbound key's first stream must be hello. Then
   `admitStream` (`internal/control/serve.go:98`) re-reads the peer row under `d.mu`:
   revoked → `revoked`, grant → `grant`, limits → `limit`. `peer.grant` takes the same
   lock and closes what the new grant forbids, and a revocation committed before the
   check refuses an open that raced it (`TestRevocationBeatsPendingOpen`, three
   interleavings). `sync` checks revocation itself and is not subject to grants.
5. **Process.** Runs as the daemon's user with the daemon's environment plus the
   opener's `env`, then `BEAM_*` last, so the caller id cannot be spoofed. The group
   dies with the stream (D30).
6. **Local socket.** `0600` under a `0700` `run/`; any process as that user is the
   owner. Attach ids are 128 random bits.

The trust table of 01 holds. Findings:

- **medium — a revoked machine never learns it was revoked, dials forever and tells
  its user mail will be delivered.** Every peer refuses its hello with `revoked`
  (`internal/control/peers.go:116` logs `dial …: refused: revoked` on each attempt,
  17 lines in four minutes here, then every five minutes forever). The daemon has no
  way to receive the signed revocation, because sync never opens, so `beam status`
  says `0 revoked`, `beam peers` says every peer is `offline`, and `beam msg send`
  says "beam will deliver it when alpha connects". Not a security gap (the revoked key
  gets nothing) but the one place where the machine's own view is wrong and the user
  cannot tell why. Fix: when a hello is refused `revoked`, record it on the peer
  state and show it (`state: "refused"` or a `status.revokedBy` count), stop the
  dial loop for that peer at the maximum backoff, and answer `msg.send` to a peer that
  refused us `revoked` with `rejected` rather than `stored`. The refusal is
  unauthenticated, so treat it as advice, not as a record.
- **medium — `admission.tunnels` grows by one record per client key ever seen and is
  never pruned.** `internal/transport/admission.go:102` adds a record for every new
  key; a failed or superseded one becomes `dead` at lines 125 and 193 and stays. Each
  reconnect of an honest peer uses a fresh ephemeral key (03), so a fleet of five
  reconnecting on the 5-minute backoff for a week leaves about 10,000 dead records;
  a holder of a leaked address can add one per handshake. Sixty-some bytes each, so
  not a fast leak, but unbounded by design ("remember `C` as failed", 03). Fix: drop
  dead records when their tailcat peer disappears from `Server.Status()`, or cap the
  dead set (a ring of the last N) and update 03 to say so.
- **low — `acknowledgedBy` can block a sync loop forever.**
  `internal/control/ceremonies.go:168` sizes the ack channel by `len(d.peers)` and then
  broadcasts outside the lock; a peer pinned in between makes `broadcast` send to more
  peers than the channel holds, and after the 5 s deadline nobody drains it, so the
  extra pong blocks at `internal/control/sync.go:128` until the peer's tunnel dies of
  silence. Fix: size the channel inside `broadcast` under the lock, or make the send
  non-blocking.
- **low — the worker has no cap on fleets.** `worker/src/index.ts:66` registers a
  fleet for anyone with any passkey, rate-limited per fleet id only, at 5,000 × 8 KiB
  each. That is a D1 bill, not a beam security matter. A global limit or a
  per-IP rate on `POST /v1/fleets` is cheap.
- **low — `GET /v1/entries` rate-limits only after a fleet is found**
  (`worker/src/index.ts:96`), so unknown tokens are unlimited D1 lookups. 256-bit
  tokens make guessing hopeless; the cost is only load. Limit on a hash of the token
  before the lookup.
- **low — a re-join while streams are attached ends them without saying why.**
  `internal/control/enrolment.go:65` ends the old enrolment whole (node closed) before
  the new one starts, so every attached shell closes `connection-lost`. Documented
  nowhere. Either state it in 02 (`join` on an enrolled machine restarts the
  transport) or reuse the node when the key and address are unchanged.
- **note — `fleet.reset` is not a ceremony and does not check `busy`.** A
  `revoke.start` followed by `fleet.reset` then `revoke.wait` fails with an internal
  error from the closed store; nothing is applied and nothing leaks. Fine, but
  `fleet.reset` could refuse with `busy` while a flow exists.

Things I looked for and did not find: a control op reaching a stream without
`admitStream` (none; `pty.open`/`exec.open` reserve only and the acceptor checks per
open); a directory entry the worker accepts that a daemon treats differently from a
synced one (none; both go through `ParseRecord` and `Verify`); a way to inject `BEAM_*`
(none); a way to open a stream on a tunnel the peer did not dial (none; the acceptor
never opens); a stale enrolment surviving a swap (none; every `enrolledOp` captures
`e` at arrival and the old node and store are closed); an assertion for one domain
accepted in another (none; the worker and the daemon both derive the challenge from
`kind`).

## 3. Simplicity

The tree honours the rule better than most: one transport, one socket, one store, no
fallbacks, no flag nobody sets (`--derp-map` and `--detach` are both used). What is
left over:

- **medium — two directory reads per join** (section 1, 01). About 30 lines could go
  if `enroll` took the records it should learn.
- **medium — two clocks for one ceremony timeout** (section 1, 02). One of
  `flow.go:33` or `ceremony.go:107` is redundant and the redundancy is the bug.
- **low — seven test hooks in production code.** `internal/control/hook.go:8` (a
  process-global with six named points: `admitting`, `admitted`, `attached`,
  `subscribing`, `dumped`, `revoking`), `internal/transport/node.go:43`
  (`beforeRegister`) and `internal/transport/admission.go:43` (`hook`, two points).
  Each is used by at least one race test that could not be written otherwise
  (`TestRevocationBeatsPendingOpen`, `TestPinnedBeforeOK`,
  `TestRecordLearnedDuringDump`, `TestSubscribeRacingDisconnect`,
  `TestCloseAtRegistration`, `TestOverlappingHellos`), so each earns its place. The
  cost is 25 lines and `d.at` calls sprinkled at six sites. Keep, but fold the two
  transport hooks into the same shape as control's (one function, named points) so
  there is one mechanism rather than three.
- **low — `stream.KindHello` is defined and never used** (`internal/stream/codec.go:53`);
  `internal/transport/admission.go:267` and `internal/transport/node.go:190` use the
  literal. Use the constant or delete it.
- **low — two JSON marshalers.** `stream.Marshal` (no HTML or JS escaping, the rule of
  D34) for everything on the socket, and `encoding/json` for records in
  `internal/control/sync.go:156` and `internal/store/store.go:90`, which escapes `<`
  in a label to `<`. Harmless because every reader canonicalises, but it is two
  ways to write JSON. Use `stream.Marshal` everywhere or say why records differ.
- **low — `status.ready`** (section 1, 06): a field with one value.
- **low — `beam revoke` makes two extra socket calls for its output line**
  (`internal/cli/enrol.go:74`: `peer.resolve` and a full `peers` page), and `beam init`
  calls `status` to learn its own label (`internal/cli/enrol.go:35`). Returning `label`
  from `init.wait` and `revoke.wait` removes both.
- **low — `PeerView` costs a `Server.Status()` and three `count(*)` queries per peer**
  (`internal/control/ops.go:145`, `Path`), and `status` builds every view just to
  count states. Fine at five peers; a `peers` call at fifty is fifty tailcat status
  snapshots. Compute `Path` and queue counts once per call.
- **low — SQLite calls under `d.mu`.** `admitStream` (`serve.go:102`) and `opGrant`
  (`ops.go:222`) hold the daemon lock across a query so that a grant change and an
  open serialise, which is the intended invariant; with `busy_timeout(5000)` a
  contended write can hold every other op for up to 5 s. Acceptable for one person's
  machines; worth a comment saying it is deliberate.
- **low — five refusal types.** `identity.Refusal`, `transport.Refused`,
  `directory.Refused`, `control.OpError`, `mailbox` reason strings. Each is its layer's
  wire token and the mapping (`refusal`, `ceremonyErr`) is explicit, so this is
  layering, not duplication. Noted so nobody adds a sixth.
- **note — the flat `request` struct** (`internal/control/socket.go:22`, 33 fields for
  25 ops) is the simplest thing that works and I would not change it.

Nothing left from a superseded approach: no `MSK`, no derived signing key, no HMAC
auth, no `forget`, no second transport. `fakeworker` duplicates the worker by design
and the contract test in CI keeps them aligned.

## 4. Tests

**What `docs/10` promises versus what exists.** The vector list matches the spec's
list one for one (33 record vectors including every failing check named). JCS has its
own vectors. HKDF is pinned (`TestDirectoryKeys`). Property tests exist for frames under
arbitrary splits, header lines around the cap, and mailbox crash points with SQLite's
commit hook (`TestCrashPoints`, 16 s). The npm shim and `pack.mjs` have node tests. The
browser callback runs real Chromium with a virtual authenticator. The contract test
runs the Go client against the worker under `wrangler dev` in CI. Of the fifteen
integration rows, thirteen have a daemon-level scenario with the row named in the
comment; "leaked address, no entry" and half of "possession" live in
`internal/transport` against bare nodes rather than daemons, which is the right level
for them. "Table tests for every table in the spec" (`docs/10:10`) is overstated: the
grants table, the outcomes table and the transactions table are covered by scenarios,
not table tests. Say "every table has a test".

**Breaking three things.**

| Broken | Covering test | Result |
|---|---|---|
| Possession MAC never compared (`admission.go:284`) | `TestPossession`, `TestPossessionBeforeSideEffects` | both fail |
| Grants always allow (`serve.go`, `allows`) | `TestGrants` | fails |
| Hello ignores revocations (`serve.go:185` verifies with a `false` predicate) | `TestRevokeLive`, `TestRevocationOnLiveSync`, `TestJoinWhileRevocationWithheld` | **all pass** |

- **medium — admission step 3 of `docs/03` (refuse a revoked entry at hello) has no
  test that fails without it.** With the check removed, the revoked dialer is admitted,
  bound and pinned, and only its `sync` open is refused (`serveSync` checks again), so
  every scenario still sees the peer refused. The second line of defence hides the
  first. Fix: in `TestRevocationOnLiveSync`, dial from the revoked machine with a bare
  node and assert the hello response is `{ok:false, reason:"revoked"}`, as
  `TestPossession` does for `possession`.
- **medium — no scenario sends mail across a `fleet reset`**, which is how the
  high finding in section 1 survived. `TestFleetReset` re-joins and checks connection
  only.
- **low — nothing exercises `ceremony-timeout`**, which is how the token bug
  survived.
- **low — `TestGrants` waits `2 * pingEvery()` where `pingEvery` returns one second and
  the daemon's is fifteen.** The helper's name promises the daemon's number. Rename to
  `oneExchange` or similar.

**Tautology check.** I read every integration scenario and the unit tests in
`identity`, `stream`, `mailbox` and `store`. None asserts a constant or restates its
implementation; each names the spec line it proves. `TestVersion` is the closest to
trivial and still proves that `version` starts no daemon.

**Test-only hooks in production**: seven, all earning their place (section 3).

**CI time.** The Go suite is 5:04 wall here with `-race` and coverage (packages in
parallel; `internal/cli` alone 292 s, `transport` 76 s, `mailbox` 54 s, `control` 47 s).
About 3 minutes of that is tests waiting out real daemon constants:
`TestSyncKeptWhileAnswered` 46 s, `TestConnectLossKillsSession` 34 s,
`TestFlushWriteBounded` 32 s, `TestSyncPeerStopsReading` 30 s, `TestDetachBeforeOpen`
26 s, `TestFootprint` 20 s. That is the price of having no test-only knobs for
`pongTimeout`, `writeTimeout` and `dialTimeout`, which is a defensible trade under the
owner's rule; if CI time matters more later, the one knob worth adding is a
`control.Options.Timeouts` struct the binary never sets, which would take the suite to
about two minutes. `TestFootprint` (20 s, five processes) is a measurement, not a test;
consider moving it behind `-run` or a `BEAM_FOOTPRINT` variable so `make crap` does
not pay for it on every push. Lint is 6 s; `make dist` 11 s.

## 5. Operability

Read as a user with `worker/README.md`, `beam` with no arguments, every error I could
provoke, and the daemon's log.

- **high — see section 2: a revoked machine reports `offline` peers and promises mail
  delivery.** The only trace is `dial …: refused: revoked` in `daemon.log`, which the
  user never opens because nothing told them to.
- **medium — `beam init` logs an error that is not one, twice prefixed.**
  `internal/control/directory.go:103` writes
  `directory: directory: read token opens no fleet` on every `init`, because
  `readDirectory` runs at `enroll` before `publish` has registered the fleet, and
  `ErrUnauthorized` already carries the `directory:` prefix. Fix: skip `readDirectory`
  on the `init` path (the fleet does not exist yet) and drop one prefix.
- **medium — a long `BEAM_CONFIG_DIR` fails with `bind: invalid argument`.** The Unix
  socket path limit is 108 bytes (104 on macOS); `internal/control/daemon.go:118` gets
  `EINVAL` and the user sees the raw error. The tests know this
  (`harness_test.go`, `beamDir`) and dodge it with `os.MkdirTemp`. Fix: check
  `len(path)` before `Listen` and say "socket path too long (N bytes, max 104); set
  BEAM_SOCKET".
- **medium — mail to a peer that granted us `none` is retried every two seconds,
  forever, and reported as "has not acknowledged".** The acceptor refuses the `msg`
  stream with `grant`; `internal/mailbox/flush.go:39` treats that like any open failure
  and retries at 2 s, so a new TCP stream is opened and refused thirty times a minute
  while the tunnel lives, and `msg.send` returns `no-ack` after 10 s
  (`internal/control/mail.go:111`) with no hint that the grant is the reason. Fix: a
  `grant` refusal of the `msg` open backs off to the dial backoff and surfaces as
  `pendingReason: "grant"` (or `rejected: grant`, since it will never flow until the
  other side changes its policy).
- **low — `beam init` took 22 s here before the first ceremony URL appeared**, with
  no output between `preparing network` and `waiting for your passkey (create)`; the
  two joins took under a second to the same point. I did not chase it (my harness
  lacked the test suite's `Isolate`, so netcheck may have probed the gateway), but a
  user on a real network will see the same silence during the first DERP map fetch and
  region pick. Print `choosing a relay…` or bound `homeKey` with a message.
- **low — `beam exec 3d4c -- …` says `unknown-peer: 3d4c`** for a 4-character prefix
  (`internal/control/resolve.go:35` requires eight). Say "a prefix needs 8 hex
  characters".
- **low — the headless hint uses the local hostname**
  (`internal/cli/enrol.go:167`): `ssh -L 43641:127.0.0.1:43641 doppa`. Right when the
  hostname resolves from the other machine, wrong otherwise; fine as a hint.
- **low — `daemon.log` has no line for enrolment, joins, revocations learned, or
  peers connecting.** In four minutes of a three-machine fleet the only lines were the
  bogus directory error and the revoked machine's dial refusals. A user reading the
  log after an incident gets nothing. One line each for `enrolled as … in fleet …`,
  `pinned <peer>`, `revoked <peer> (learned from <peer>|directory|here)` and
  `<peer> connected|offline` would make the log worth opening. D28 (no tailcat lines)
  stays.
- **low — `worker/README.md` is fine for the owner and thin for anyone else**: it
  does not say what `wrangler.toml`'s `ratelimits.namespace_id` is or that
  `routes.custom_domain` needs the zone on the account. Two sentences.
- **note — `beam connect` from a non-TTY stdin works** (raw mode is skipped) and the
  remote shell exits with the piped script's status. Good.
- **note — every CLI sentence in `docs/07` appears verbatim.** `revoked "gamma" on
  this machine`, `acknowledged by 1 of 1 peers`, the two `stored` sentences,
  `ambiguous-peer` listing both candidates with labels, `spawn: exec: "x": executable
  file not found in $PATH`, `killed by SIGKILL` with exit 137.

## 6. Release readiness

**Before `v0.1.0`:**

1. Keep `send_seq` across `fleet reset` (section 1, 02, high) and add mail to
   `TestFleetReset`.
2. Show a revoked machine that it was refused `revoked`, stop its dial loop's log
   spam, and reject its `msg.send` (section 2 and 5).
3. One ceremony clock so five minutes without a tap is `ceremony-timeout`
   (section 1, 02).
4. Refuse a `msg` open with `grant` at the dial backoff and say so in `msg.send`
   (section 5).
5. A hello-level test for a revoked entry (section 4), since the release claim in
   03 is "refused at admission".
6. The `init` log line and the socket-path check (section 5), both a few lines.
7. Spec refresh in one commit: binary size, `connected`'s meaning, `daemon.log` in the
   `$BEAM_DIR` listing, `events.subscribe` unenrolled, `--json` scope, the recipient/
   sender sentence in 05, `possession` off the socket error list, D24's line count,
   and the two HTTP-speaking packages. None changes behaviour; all would mislead a
   reader of the tag.
8. The manual matrix in `docs/10` ("Manual, per release": Safari, Chrome on Linux,
   phone via QR, real NAT traversal) has not been run against this tree as far as the
   repository shows. It is the only check of a real authenticator's PRF behaviour
   (open question 4) and the release notes should say which tuples passed.

**Can wait:**

- Pruning `admission.tunnels`, the ack-channel sizing, worker fleet caps and the
  pre-lookup rate limit (section 2, low).
- The second directory read at join, `status.ready`, `KindHello`, the extra CLI calls,
  per-peer `Status()` cost, the two marshalers (section 3).
- `TestFootprint` behind a switch and any timeout knob (section 4).
- Richer `daemon.log`, the prefix-length hint, the `init` wording for an existing
  fleet, `1 other machines` (section 5).
- A note in 02 on what a re-join does to attached streams.

**Nothing found that argues against the design.** The X25519 possession MAC, the
directional tunnels, the directory as ciphertext behind a passkey-verified append, and
the daemon-owned admission all do what 01 says they do, and the suite would catch
most regressions in them. The two high findings are seams between milestones, which
is exactly what a per-PR review cannot see.
