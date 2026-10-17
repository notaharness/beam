# Guided Fleet: n10 Desktop + beam.n10.is

**Design proposal for Hermann’s review. No product implementation.**
Tracks [beam #59](https://github.com/notaharness/beam/issues/59) and
[n10 #156](https://github.com/notaharness/n10/issues/156).

## Review the proposal

Open [the paired prototype](guided-fleet-ux/index.html) locally. The review toolbar selects
Create, Join, Add, Revoke, Reset and Recovery, with named states and a light/dark switch.
[Browser only](guided-fleet-ux/browser.html) and [Desktop only](guided-fleet-ux/desktop.html)
use the same states. Query parameters `?flow=create&step=1&theme=dark` select a view.
Serve this directory with any static server if file URLs are restricted.

Product buttons advance the demonstration where appropriate. **Next simulated event**
stands in for daemon events; it is outside the product frame. No credentials, live QR,
ceremony URLs, cryptography, daemon calls or network requests are used. The QR placeholder
is explicitly non-scannable. Browser delivery and daemon completion are separate clicks.
Changing a review state is not a claim that either surface can observe the other.

Source baseline: beam `9c31355`; n10 `origin/master` at
`b26a1ffba7fd44f36386eed36f92eb56107eb71c`. Read n10 through `git show`, including
`docs/design/beam-fleet-ux.md`, `apps/desktop/src/main/beam/`, Fleet components and hooks,
`components/ui`, `styles.css`, and the light/dark passkey screenshot baselines.
The n10 checkout is untouched. Beam contracts: [identity](../02-identity.md),
[control socket](../06-control-socket.md), [CLI](../07-cli.md), [Desktop](../08-desktop.md).
Worker evidence: `worker/public/index.html` and `worker/src/index.ts`.

## Placement and visual direction

| Option | Set up once / approve | Daily status / adding | Recovery / workspace continuity |
| --- | --- | --- | --- |
| Everything inline | QR, comparison and two prompts compete for sidebar width | Excellent proximity | Long errors displace machines; current 280px snapshots demonstrate the pressure |
| Everything in dialogs | Space for comparison | Repeated interruptions just to inspect machines | Workspace remains behind overlay, but routine tasks require opening it |
| **Sidebar + focused ceremony dialog** | Full comparison block and one primary action | Compact rows and add instructions stay inline | Persistent controller preserves work; dialog can be tucked away and resumed |

Recommend the third option. Fleet stays in the workspace sidebar and repository picker.
Create/join forms and active ceremonies use the existing n10 Dialog primitive, as revoke
already does. Reset stays inline because it has no browser step. No separate Fleet page.
The browser reads as the other half of the same dialog: brand, task title, location cue,
comparison block, primary action, secondary controls, then help. Avoid a marketing hero
inside a ceremony. The no-fragment homepage has its own brief introduction and entry guidance.

One **Next · …** block names where to look and what to do. The current location is explicit:
Desktop: “Next · browser or phone”; browser before approval: “Now · compare with your machine”;
browser after delivery: “Next · return to the originating machine”. For revoke, the originating
machine may differ from the target: never tell the user to return to the revoked machine.
No browser “Return to n10” deep-link button: the URL does not identify a Desktop origin,
and a phone cannot focus Desktop on another machine.

Desktop dialog **Keep working**, close and Escape hide it without cancelling. Fleet shows
“Passkey step in progress” / **Resume setup**. **Cancel request** is the distinct explicit
cancellation action. Use Radix’s supported controlled `open` state, focus management and
close handlers; keep the ceremony controller mounted above the repository gate. No custom
modal/focus trap or library bypass. After hiding, focus returns to Fleet; resume focuses the
current step heading. Automatic progress never steals focus from another application.

## Shared state and authority

| Observation | Desktop can say | Browser can say |
| --- | --- | --- |
| `*.start` returns URL | “Waiting for your passkey”; current comparison, open/copy/QR | Action and machine from the validated fragment |
| User opens prompt | Still waiting; no signal that the browser opened | “Waiting for your fleet passkey…”; disable approval; retain cancellation |
| Slot POST returns 201 | Still waiting until its own event/result arrives | “Approval sent.” / “Revocation approval sent.”; **never joined/revoked** |
| Create’s second `ceremony` event | Step 1 complete; replace link + QR; “2 of 2 · Add this machine” | First page remains “Passkey created. One more step.”; cannot discover next URL |
| `stage` event | “Finding machines…” / “Updating machines…” / “Finishing setup…” | No change; does not read the slot or poll progress |
| Successful `*.wait` | Enrolled or locally revoked; show returned publication/ack status | No change; originating machine owns completion |
| `directory.published` | Clear the matching pending notice | No change |
| Another verified `peer.new` / peer snapshot | That identity is known here; connection state separately | No change |

Create is two prompts, not one long WebAuthn request. Desktop knows which journey it started.
The browser’s `o=a` serves init step 2, join and re-join equally: title **Authorize {machine}**,
with “If you just created a fleet passkey, this is step 2 of 2.” Never show an unconditional
“2 of 2” there. Shared task terms are **Save a passkey** and **Add this machine**; retain
spec-prescribed browser titles and button labels. This proposal does not rewrite normative
ceremony copy in docs/02. Added handoff cues explain it.

Do not pretend to mirror browser prompt-open, cancellation-in-progress or delivery in Desktop.
They become visible only through daemon events/results. Likewise do not invent progress on an
existing machine while another machine joins: “Waiting for a new machine…” is all it knows.
No countdown on the page: the URL has no issue time. Say “Requests expire after five minutes.
The originating machine shows whether this request is still active.”

## Journeys and literal copy

Example names are `laptop`, `oldlaptop` and fleet `Personal machines`. Names remain selectable
text, including names containing `<` or `&`. UI uses text nodes/React text, never `innerHTML`.
Both surfaces show an identical comparison block, derived from the ceremony URL:

- **Action**: `Create fleet passkey for “{fleetName}”`, `Add “{machine}” to fleet`, or `Remove “{machine}” from fleet`.
- **Machine**: target label; aliases do not replace the signed label here.
- **Machine fingerprint**: first 16 hex characters, grouped in fours. Backend comparisons use full IDs.

The browser shows: “Continue only if you started this request just now. Compare the action,
machine name and machine fingerprint with n10 Desktop or your terminal.” Desktop shows:
“Check the action, machine name and machine fingerprint on the page.” A mismatch means do not
approve; cancel on the origin and inspect the request. Machine and fleet fingerprints are
separately labelled; the fleet fingerprint is never presented as the machine fingerprint.

### Create a fleet

| Step | Desktop / originating terminal | Browser / phone | Handoff and evidence |
| --- | --- | --- | --- |
| Choose | First run: “Your machines, connected anywhere”; **Create a fleet** / **Join a fleet**. Create form: **Machine name**, **Fleet name**, “Shown in your passkey manager.” | Homepage: “Start on the machine you want to connect.” Instructions: Desktop → Fleet, or `beam init` / `beam join`. No ceremony action. | No request yet. Name validation follows docs/02; retain invalid input, use defaults for blank input in production. |
| Explain | “Save one passkey for your fleet, then use it to add this machine. Expect two passkey prompts.” **Continue** starts `init.start` + `init.wait`. | No change until link opens. | Starting/preparing network has its own status; do not show a stale link. |
| Prompt 1 | “1 of 2 · Save a passkey”; “Next · browser or phone”; “Save the passkey there. Come back here for the second link.” Comparison block, **Open in browser**, **Copy link**, QR, **Cancel request**. | “Step 1 of 2 · Create your fleet passkey”; “First save a passkey. Then return to the machine where you started and open its new link to add this machine.” **Create passkey**. | Open browser when URL arrives, as today. QR is the same URL byte for byte; no different phone flow. |
| Prompt 1 delivered | Remain on step 1 until second `ceremony` event. | “Passkey created. One more step.” / “In n10 Desktop or your terminal, open the new link or scan the new QR code. Use the same passkey to add this machine.” | Acknowledges creation and delivery only. First tab is finished; it cannot open or redirect to step 2. |
| Prompt 2 | On second event: “2 of 2 · Add this machine”. “Use the passkey you just saved.” Replace the old URL and QR; open new URL. If still on phone, return and scan the **new** QR. | New page: “Authorize laptop”. “Use your fleet’s passkey to add this machine. If you just created a fleet passkey, this is step 2 of 2.” **Authorize machine**. | Never reuse first URL; no back button that restarts its slot. |
| Prompt 2 delivered | Waiting, then “Finishing setup…” only when `publishing` arrives. | “Approval sent.” / “Return to laptop in n10 Desktop or your terminal to check whether it joined.” | Browser explains it cannot confirm the result. No success checkmark for membership. |
| Complete | Only `init.wait` success: close dialog, announce “Fleet created”, show local row, and “Your other machines will appear here.” **Add a machine** via Fleet +. | Remains “Approval sent.” Can close this tab. | Publication status is independent of membership. Pending notice persists after dialog closes. |

A created passkey can remain saved after failed setup. Do not imply rollback deleted it.
No resume-at-step-2 API exists after the operation fails; a fresh create starts both prompts.

### Add a machine from an existing member

| Step | Existing Desktop | Joining Desktop / terminal | Browser |
| --- | --- | --- | --- |
| Open instructions | Fleet **+** → “Add a machine”. “Next · your other machine”. “Open n10 Desktop → Fleet → Join a fleet. Use your existing fleet passkey.” | Not involved until the user goes there. | No request; do not generate an invitation on the existing machine. |
| Headless alternative | “Or run this on the other machine:” `beam join --label my-server`; **Copy command**. “Open its link or scan its QR code.” | Run the command **there**. It generates that machine’s request. | A phone may scan the terminal QR; same security comparison. |
| Compare | “Compare its fleet fingerprint with this:” plus grouped fleet fingerprint and **Copy fleet fingerprint**. “Waiting for a new machine…” | Follow the join sequence below, including the final fleet comparison. | Cannot know existing member’s fingerprint or join outcome. |
| Observed | On a newly observed verified member ID: close instructions, announce “{machine} joined”, show row with actual Connected / Offline state. | Join’s wait result remains its source of completion. | Unchanged. |

Snapshot known IDs when instructions open; renames, reconnects and known identities do not
complete adding. Tracking survives collapse/repository switching. If the panel closes, Fleet …
still exposes the fleet fingerprint. There is no server-side invite, pairing PIN or fleet picker.
An existing machine may not hear about a join while disconnected; close instructions is always
available. Do not poll the remote directory for a progress signal.

### Join from another machine

| Step | Joining Desktop | Existing Desktop | Browser |
| --- | --- | --- | --- |
| Prepare | **Join a fleet**, **Machine name**. “Use the passkey for your existing fleet. This takes one passkey prompt.” | Add instructions or Fleet overview. | Homepage until URL opens. |
| Authorize | `join.start` / `join.wait`; “Add this machine”; comparison + open/copy/QR. “Use your fleet’s passkey there. Come back here to see the result.” | “Waiting for a new machine…”; no fictional signing progress. | “Authorize {machine}”; **Authorize machine**; one `get`, using existing passkey. |
| Delivered / processing | “Finding machines…” on `reading directory`; “Finishing setup…” on `publishing`. | Still waiting unless a new verified member is actually observed. | “Approval sent.” Return to origin. |
| Joined | `join.wait` success: “Check fleet fingerprint”. “This machine joined. Before using remote connections, compare with a machine already in your fleet: Fleet … → Copy fleet fingerprint.” Show grouped **Fleet fingerprint**, **Matches** / **Doesn’t match**. | Show fingerprint in Add instructions or Fleet …; new row only from peer data. | Remains terminal. |
| Match | Dismiss comparison, keep rows and publication status. | Normal Fleet. | Can close. |
| Mismatch | “Different fleet”. “Stop using remote connections. Reset here, then join with your fleet’s passkey.” **Reset fleet…**. | Its state is unaffected. | Cannot determine mismatch. |

The comparison is a human check of the root adopted on first join. It is **not a transport gate**:
the daemon has already enrolled. Keep the comparison pending through collapse, hidden dialogs
and repository changes for this enrolment; do not imply an unimplemented pre-connection gate.
Persisting the check across app restarts would need Desktop-local storage keyed by full fleet
and peer IDs (optional; not assumed here). Re-join uses the same browser page; a pinned daemon
checks the expected root without asking the page to identify the fleet.

### Revoke

| Step | Originating Desktop | Browser | Reflection |
| --- | --- | --- | --- |
| Confirm | Target row → **Revoke access…**. “Revoke access to {machine}?” Target fingerprint. “This permanently revokes this machine identity. Files and programs remain. Offline machines learn when they reconnect. Reset cannot restore revoked access.” **Revoke access** / **Cancel**. | No request yet. | Resolve by full ID; duplicate labels are not sufficient. |
| Approve | `revoke.start` / `revoke.wait`; focused dialog with removal comparison; **Cancel request**. | “Remove {machine} from your fleet”; identical Action. **Authorize removal** with destructive treatment. | Browser explains permanent revocation and offline propagation. |
| Delivered | Still waiting; then “Updating machines…” on `notifying peers`, “Finishing setup…” on `publishing`. | “Revocation approval sent.” / “Return to your originating machine to check that the revocation completed and see its publication and acknowledgement status.” | Never call it revoked on slot delivery. |
| Complete | Wait success closes dialog; row “Revoked”; announce “Access revoked here”. “Acknowledged by {n} peers. Offline peers learn when they reconnect.” Publication **Published** or pending notice. | Remains “Revocation approval sent.” | `acknowledgedBy` is result-time evidence, not a live all-devices counter. No undo. |

A revoked identity cannot rejoin after fleet reset. Returning requires a new node key and peer
ID; do not offer an automatic “Reset and rejoin” remedy for `revoked-peer`. The UI does not
silently delete identity files. Revocation prevents access as specified, not remote data deletion.

### Reset and lost passkey

| Step | Desktop | Browser | Reflection |
| --- | --- | --- | --- |
| Confirm | Fleet … → **Reset fleet…**. Inline “Reset fleet here?” / “Disconnect this machine and delete queued messages to its old peers. Other machines and your passkey stay unchanged.” / “This keeps this machine’s identity. It will not recover a lost passkey or undo a revocation.” | No browser step or passkey prompt. | Reset is local; not fleet deletion and not revocation. |
| Submit | **Type reset to confirm**; **Reset fleet** disabled until exactly `reset`; call `fleet.reset {confirm:"reset"}`. “Resetting fleet…”; prevent double submission. | None. | Existing streams close; no success until socket result. |
| Complete | “Fleet reset here”; first-run **Create a fleet** / **Join a fleet**. | Any old tab stays terminal/stale; do not reuse it. | Drop stale local comparison/publication UI with enrolment change. |
| Failure | “Couldn’t reset fleet.” plus mapped explanation and **Details**; keep typed confirmation; allow retry/cancel. | None. | Re-read status after connection loss before offering retry. |

Lost passkey help: “Existing connections can keep working, but you cannot add or revoke machines.
To start over, reset and create a new fleet on one machine. Then reset and join that fleet on each
other machine.” This creates a new root; it does not restore the old passkey or transfer trust.

## Failure and recovery contract

Browser outcomes retain docs/02’s distinctions. **Technical details** shows WebAuthn result and
missing PRF field / exception name separately from HTTP delivery. No secrets or ceremony URLs
in telemetry, diagnostics, screenshots or reports. Cancellation races yield one result and one
slot write. No same-slot retry after any terminal delivery attempt, including lost responses.

| Condition | Browser copy / action | Desktop copy / action |
| --- | --- | --- |
| Invalid fragment | “This link is incomplete or invalid.” Return for a fresh link; no approval/cancel/slot write. | Do not guess its cause from silence; current request may still wait. |
| Missing WebAuthn | “This browser cannot use passkeys.” Copy link; no approval. Cancellation only if sealing works. | Still waiting; can cancel and start fresh. |
| Missing X25519 / preflight sealing | “This browser cannot encrypt the answer.” Explain browser requirement; Copy link; no approval or cancellation write. | Still waiting; cancel here if changing request. |
| PRF capability unknown | Permit approval; do not claim provider support. | Wait normally. |
| Browser reports PRF false | Explain provider may handle it separately; explicit **Try anyway**, then approval; Copy link / Cancel when seal-capable. | Wait normally. |
| PRF missing from result | “This passkey did not provide WebAuthn PRF.” Compatibility disclosure opens; return for fresh request. For create: “A passkey may have been saved, but fleet creation is not complete.” For join/revoke: “Use the same fleet passkey; a new passkey creates a different fleet.” | `prf-unsupported`: same explanation and **Passkey help**; fresh request using compatible browser/provider. |
| Cancel / NotAllowedError | “Passkey request cancelled or not allowed.” Explain cancel, timeout or no eligible passkey; do not infer which. | On `ceremony-cancelled`: “Passkey request cancelled. No further approval is pending for this request.” Fresh request. |
| Other WebAuthn failure/null credential | “The passkey request failed.” Return for fresh request; sealed `failed`. | `bad-assertion` explains failed/unverifiable answer, not presumed PRF failure. |
| HTTP 201 | Appropriate delivered heading, even when result is cancelled/failed. | Only its wait result determines success/failure. |
| HTTP 409 | “This request was already answered.” Check origin; if it enrolled without your approval, reset there. Reset cannot undo revocation. | May get `ceremony-state`; inspect status, then new request. No claim that compromise is established. |
| No response within 20s | “Could not confirm delivery.” Check origin before retry; if still waiting cancel there. Nothing is replayed. | May still finish. Re-read status on reconnect; do not start a duplicate automatically. |
| HTTP 429 | “Too many requests.” Wait a minute, then cancel at origin and start fresh. | No invented browser error event; may still be waiting. |
| Other HTTP rejection | “beam.n10.is did not accept the answer (HTTP {status}).” Return to origin. | Cancel still-pending operation before fresh start. |
| Sealing fails after prompt | “Could not encrypt the answer.” “Nothing was sent from this attempt.” | Cancel at origin; use supported browser for fresh request. |
| Five-minute expiry | Page cannot establish expiry; retain generic lifetime guidance. | `ceremony-timeout`: “This passkey request expired after five minutes.” **Start a fresh request**. |
| Wrong passkey | Page may still say Approval sent. | `wrong-passkey`: “This passkey does not unlock the expected fleet. Choose the original fleet passkey.” |
| Directory read fails | Page may still say Approval sent. | `directory-unavailable`: “Cannot read the fleet directory. Check the connection to beam.n10.is and try joining again.” |
| Connection lost / daemon restarts | Page unchanged; cannot know. | “Reconnecting… Status may be out of date.” Disable mutations; re-read `status`/peers before retry, since operation may have completed. |
| Storage / malformed result / internal | Page’s known result only. | `storage-failure`: check disk/permissions. `bad-entry`, `ceremony-state`, `internal`: inspect details and current status before fresh request. |
| Stale action / concurrent operation | Page’s known result only. | `busy`: finish/cancel existing request. `already-enrolled`: show status. `not-enrolled`: create/join. `unknown-peer`: refresh rows. `ambiguous-peer`: choose fingerprint. `params`: correct names without clearing form. `revoked-peer`: identity cannot be restored by reset. |

Pending publication copy: “Saved here. Waiting to sync…” plus “Directory publication is pending;
beam will retry while it runs.” Observe matching `directory.published {kind,peerId}` and scope to
the enrolment generation; do not let an unrelated event clear it. The current socket does not
provide a durable pending-write inventory or a publication-failed event. After app/daemon restart,
do not fabricate either “Published” or “still retrying” from `status`; show only known state.
A permanently refused write may be dropped and logged: an observed pending notice cannot promise
that every write will eventually publish. Richer durable diagnostics are separate work.

## Visual language and static delivery

| n10 source / token | Light | Dark | Use on beam page |
| --- | --- | --- | --- |
| `--background` / `--foreground` | `#fff` / `#1f1f1f` | `#1f1f1f` / `#ccc` | Page and main text |
| `--muted` / `--muted-foreground` | `#f3f3f3` / `#616161` | `#2b2b2b` / `#9d9d9d` | Comparison panel, help and secondary copy |
| `--primary` / `--primary-foreground` | `#005fb8` / white | `#0078d4` / white | Single primary action, active step and focus |
| `--border` / `--input` | `#e5e5e5` / `#cecece` | `#2b2b2b` / `#3c3c3c` | Quiet separators and control edges |
| `--sidebar` / `--popover` | `#f8f8f8` / white | `#181818` / `#222` | Desktop shell / dialog; browser uses card surface |
| `--success` / `--warning` | `#1a7f37` / `#9a6700` | `#3fb950` / `#d29922` | Evidence-based state with text, never colour alone |
| `--destructive` | `#cf222e` | `#f85149` | Removal and reset; outlined red text avoids small white text on dark theme’s light red fill |
| `--radius` | 6px | 6px | Controls; 8px dialog/panel outer edge |

Use n10’s `styles.css` system sans stack and mono stack; no downloadable font. Type scale
11 / 12 / 13 / 15 / 18 / 22px: 22 task heading, 13 body, 12 secondary, 11 labels, 13 mono
fingerprints. Browser touch controls at least 44px high in implementation; mobile form inputs
at least 16px to avoid zoom. Desktop keeps its compact native 28/36px buttons. 4px spacing
base, 16px related groups, 24px panel padding, 440px readable browser content width. Permit
wrapping for names, errors and fingerprints without truncating security information.

Map `Button` primary/outline/ghost, `Input` + `Label`, `Dialog`, `Banner`, disclosure and
`Badge` treatments to semantic HTML/CSS. Use actual n10 primitives in Desktop; static page
uses `<button>`, `<label>`, `<dl>`, `<details>`, headings, and status/alert regions, not React
or a copied component runtime. Preserve native accessibility. The mockup approximates product
layout; it is not a replacement for Radix keyboard/focus behavior.

For implementation, copy the small named token subset from a recorded n10 commit into a clearly
marked CSS section of `worker/public/index.html`. Keep one CSS variable vocabulary and include
light/dark definitions; default via `prefers-color-scheme`, optional theme selector. No runtime
fetch from n10, Tailwind compilation, shared package or framework required. Changes to n10 tokens
require a paired visual review and updating this documented snapshot. Worker CSP hashes must be
regenerated for the edited inline style/script; retain current slot-only `connect-src` and no
external assets. This phase does not edit the asset, CSP or worker configuration.

Accessibility: focus the new heading on a user-initiated step change; announce asynchronous
state once with `aria-live="polite"`, errors with `role="alert"`; button disabled while prompt
runs; text status supplements icons. Native disclosures hide dense compatibility details until
needed, PRF error opens them. Dark/light follow each device independently, no theme parameter in
ceremony URLs. Reduced motion removes decorative packets/spinners’ motion without hiding status.
At 390px and 200% zoom the page is a single column, scrollable, with no horizontal clipping.
Preserve homepage machine illustration and reduced-motion behavior in implementation; the prototype
uses a simple static diagram. Preserve the documented compatibility matrix and its unverified
qualifiers; this design does not introduce new vendor-support claims.

## Additions and spec impact

**No protocol, URL-fragment, worker endpoint or control-socket additions are required.**
Use existing start/wait, stage, ceremony, cancel, status/peers, peer events, reset and publication
events. Desktop already has `main/beam/ceremony.ts` mapping these to host progress/outcomes.
UI work: dialog placement and persistent visibility state, shared copy, sidebar resume action,
progress/failure presentation and static page styles. No live browser-to-Desktop progress channel.

The proposal changes n10’s current design note placement (create/join inline → dialog) and adds
explicit handoff copy. If approved, update that note and beam docs/08’s “one card” description in
the corresponding implementation PRs. Security behavior and docs/02’s required headings remain
contracts. This design does not silently change them. Desktop-local persistence of post-join
comparison is optional as described above. A pre-enrolment human approval gate, automatic page
step-2 transition, live cross-device progress, or durable publication diagnostics would require
separately designed protocol/socket changes and are excluded.

## Questions for Hermann

1. **Approve sidebar status + focused passkey dialogs?** Recommend yes: routine work stays inline,
   while the full comparison and QR fit at useful sizes. Keep working tucks the dialog away.
2. **Keep explicit return-and-open for creation’s second prompt?** Recommend yes, with the new
   Desktop link opening automatically as today and phone users rescanning the new QR. This needs
   no protocol change; first browser page never pretends it can advance itself.
3. **Persist the post-join fingerprint check across Desktop restarts?** Recommend yes in the n10
   implementation, keyed to full fleet + local peer identity and cleared on reset/re-enrolment.
   It remains a human reminder, not a transport gate. The mockup models one session only.
4. **Use beam branding with an “n10 Fleet” context label?** Recommend yes: match n10’s design
   language, retain beam.n10.is as the relying party, and keep terminal/phone copy first-class.
5. **Carry the token subset by a reviewed snapshot?** Recommend yes for this static asset.
   Revisit a shared generated token artifact only if more surfaces need it.

## Review coverage and limits

The prototype covers all six journeys, independent delivery/completion, second create link,
fleet comparison/mismatch, pending publication, permanent revoke, typed reset, and representative
recovery/preflight states in both themes. The failure table is the complete copy/behavior contract;
not every HTTP status has a separate clickable frame. No OS passkey dialog is imitated.
Screenshots live in [guided-fleet-ux/screenshots](guided-fleet-ux/screenshots/). `capture.cjs`
records the selected views with an installed Playwright Chromium and checks all state renders,
form injection safety, reset confirmation, and browser terminal wording. These are prototype
checks, not assertions of real PRF/provider compatibility or implemented cross-app behavior.

Implementation follows review and is split between beam and n10. No product code changes,
protocol changes, real daemon tests, publishing or deployment belong to this phase.
