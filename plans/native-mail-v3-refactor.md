# Mail 3.0 refactor: living implementation plan

## Objective

Organize the core around explicit read, composition, and mutation workflows and
achieve the maximum verified safe reduction in meaningful production Go lines.
Preserve every exported API, CLI/TUI interaction, capability, output and error
contract, reliability guarantee, and performance characteristic.

This file is the working record for progress, todos, decisions, findings, and
validation. Update it after each integrated stage and whenever a material
constraint or decision changes. An unchecked acceptance gate remains required
work. Line-count targets never justify weaker guards or denser formatting.

## Baseline and delivery

- Baseline commit: `8271f9b869cd2ad2e7b98454607387b4c52e3fc2`.
- Working branch: `0xSMW/mail-native-v3`, in its existing managed worktree.
- Delivery: open a new regular PR against `master`, with per-file commits and
  aggregate results. The user selected this after PR #24 closed.
- Version: retain 3.0.0 and output schema version 1.
- Production Go baseline: 26,327 formatted physical lines, including embedded
  scripts. `pkg/mail`: 15,640 lines and 518 functions.
- Largest coordinators: `RunBatch` 378 lines, `VerifyMutations` 271 lines,
  `composeNativeOutgoingOwned` 248 lines. Aim for roughly 100 lines or fewer per
  coordinator, with explicit domain responsibilities in the extracted steps.
- The user revised the initial 2,000-line target after the integrated source
  measurement and second audit. The accepted criterion is the design improvement
  plus maximum verified safe reduction, with actual counts reported.
- Count all replacement code. Exclude tests, documentation, comments, blanks,
  minification, generated code, and moving code into assets from reduction credit.
- Use only GPT-6.1 Sol agents at high reasoning for delegated work.
- Private binaries, source hashes, fixture data, and detailed validation evidence
  remain outside Git. Record only aggregate findings here and in the PR.
- Merging, releasing, tagging, and installing require a subsequent request.

## Design

Keep `Client` as the public facade. Share equivalent policy, decoding, data
preparation, and outcome bookkeeping. Keep native and legacy transactions
explicit and coarse enough to preserve batching, lock ownership, and process
counts. Avoid a universal backend interface or a script-template framework.

### Native descriptor and projection vocabulary

- [x] Share descriptor list/string/integer/boolean/reference decoding where
      contracts match, preserving contextual errors.
- [x] Share bulk account projection, identity/order brackets, and duplicate-ID
      checks between discovery and sender selection.
- [x] Keep sender selection pure after each fresh projected observation.
- [x] Consolidate recipient and attachment projections without adding getters.
- [x] Preserve operation-specific Apple error-code policies exactly.
- [x] Remove only proven unused private paths; retain exported entry points.

### Read workflows, MIME, and index policy

- [x] Share single-message and batch MIME enrichment and diagnostics.
- [x] Share mailbox selection preference and membership SQL while preserving
      physical ownership versus label membership.
- [x] Share index fallback eligibility and warning policy where equivalent.
- [x] Retain the single-statement source/destination verification snapshot.
- [x] Preserve streaming, size/depth limits, charset behavior, diagnostics,
      partial-file handling, and the stricter stored-draft reconstruction policy.
- [x] Keep read concurrency, query pooling, and local-content fast paths intact.

### Mutation orchestration and receipts

- [x] Introduce an invocation-owned batch runner with explicit preparation,
      dispatch, recording, verification, and completion responsibilities.
- [x] Consolidate repeated status/counter/journal bookkeeping.
- [x] Make accepted, uncertain, verified, and cleanup states distinguishable.
- [x] Simplify verification sequencing without changing observation schedules,
      early completion rules, final-observation semantics, or sync behavior.
- [x] Retarget tests of obsolete private algorithms onto production workflows
      before removing those algorithms.

### Composition and draft preparation

- [x] Share outgoing input validation across ordinary and threaded composition.
- [x] Share prepared-draft data and equivalent replacement preparation.
- [x] Simplify the native composition coordinator into named responsibilities.
- [x] Preserve each backend's current rich-draft, whitespace, recipient, sender,
      attachment, cleanup, and result contracts.
- [x] Keep atomic compare-and-delete inside the legacy bridge where required.

### Legacy scaffolding and CLI ownership

- [x] Consolidate script execution, JSON decoding, status parsing, and recurring
      message projections without changing script process or Mail-event counts.
- [x] Keep strict-ID, bounded-predicate, ordinary-detail, and absence-proof
      lookup contracts distinct.
- [x] Replace mutable global invocation ownership with local application/options
      state and fresh command construction, preserving repeated `Run` behavior.
- [x] Share equivalent flags, mailbox-name tables, cache-loading glue, and
      presentation inputs while retaining per-command defaults and validation.
- [x] Preserve TUI request lanes, write queue, optimistic state, selection,
      navigation, rendering, and late-response handling.

## Invariants

1. Native failures never replay a write through legacy scripting.
2. Send dispatch happens at most once. Lost replies and interrupted writes keep
   uncertainty and do not authorize retries.
3. Account, mailbox, source message, sender ownership, recipients, and attachment
   bytes retain exact checks and fresh observations where currently required.
4. Gmail INBOX archive uses Mail's Archive action with owned-viewer checks.
   Label membership and generic move semantics remain distinct.
5. Automation locks cover resolution, dispatch, readback, and bounded cleanup.
   Local SQLite/MIME reads remain outside the automation gate.
6. SQLite remains read-only with exclusive leases, short snapshots, cancellation
   cleanup, and at most four pooled readers. Local MIME work remains bounded.
7. A failed read never proves absence. Permission/schema/malformed-data errors
   keep their current failure and fallback classification.
8. Receipts remain durable and visible after side effects. Supplemental failures
   cannot erase an applied or uncertain result.
9. Archive settling and mutation verification windows retain their duration and
   scheduled observations. Reconciliation remains read-only.
10. All exported methods, command names, flags, aliases, JSON fields, exit codes,
    help text, native/legacy/non-cgo capabilities, and TUI interactions remain.

## Execution stages

- [x] Confirm clean 3.0 worktree and freeze baseline source/hashes.
- [x] Save this living plan and create the active goal.
- [x] Build immutable baseline binaries and capture initial contracts/metrics.
- [x] Integrate native primitives and local read/index consolidation.
- [x] Integrate legacy scaffolding and composition preparation consolidation.
- [x] Integrate mutation workflow and receipt simplification.
- [x] Integrate CLI ownership and equivalent presentation-input cleanup.
- [x] Inspect integrated diff and measure actual meaningful reduction.
- [x] Resolve scoped independent review findings.
- [x] Complete final validation, commit/push owned changes, update PR results,
      and inspect review status.

## Acceptance and validation

### Offline behavior and failure parity

- [x] Exported API compatibility, command surface snapshot, and representative
      JSON/plain/help/error/exit-code contracts match the frozen baseline.
- [x] Reads, search completeness, pagination, scan checkpoints, rules,
      signatures, attachments, drafts, threading, mutations, and reconciliation
      retain their capabilities and backend-specific semantics.
- [x] Permission failures, cancellation before/after dispatch, lost replies,
      stale/reordered IDs, incomplete MIME, corrupt attachment bytes, and journal
      failures preserve results and actual mutation attempt counts.
- [x] Full Go suite, vet, targeted race checks, non-cgo compatibility, installer,
      surface, and offline validation-script checks pass.
- [x] Native Apple Silicon and Intel builds pass.

### Performance

- [x] Compare frozen 3.0 native to refactored native and frozen 3.0 legacy to
      refactored legacy, with randomized paired observations.
- [x] Reuse accounts, mailboxes, lists, cached detail/body batches, scoped/broad
      search, and index-unavailable workloads. Keep initial and warmed series
      distinct; Mail remains running during fresh CLI-process observations.
- [x] Compare p50/p95 wall time, CPU, memory, native events, SQLite queries,
      local-file extraction, and subprocess counts. Investigate variation with
      repeated controlled samples; any reproducible regression blocks completion.
- [x] Complete cached bodies retain zero Mail content requests. Native commands
      retain zero scripting/SQLite subprocesses. Batching and fresh guard event
      counts do not increase.

### TUI and live acceptance

- [x] Replay navigation, selection, scrolling, search, compose/reply-all,
      optimistic mutations, delayed refresh, cancellation, and queued-write quit
      behavior at fixed terminal sizes.
- [x] Verify disposable draft/save/direct-send/reply/forward/attachment/flag/
      move/archive behavior and fixture cleanup locally. Record rich stored-draft
      refusal explicitly; successful plain stored-draft update/send remains
      deterministic coverage, not a live macOS 27 claim.
- [x] Run home-server macOS 27 build/fixture checks.
- [x] Verify home-server Terminal Automation, Full Disk Access, and Accessibility.
      Complete reversible setup without further user questions; live acceptance
      and cleanup are tracked separately above.

## Progress and notes

### 2026-10-14: started

- Baseline worktree is clean at `8271f9b`; existing PR #24 is attached.
- The prior independent Fable xhigh analysis supports roughly 1,000–1,350 lines
  of direct deduplication. The 2,000-line goal also requires workflow and local
  enrichment simplification; it remains an acceptance target, not a result.
- Preserve current behavior when apparent duplication protects different
  contracts. Do not broaden Apple error fallback sets, strengthen legacy rich
  draft refusal, or remove public methods as incidental cleanup.
- The existing TUI request-lane and write-queue abstractions are sound; reuse
  them and keep visible interaction stable.
- Home-server live tests previously failed Automation (`-1744`) and Full Disk
  Access. The user has offered to enable testing when requested.
- Concrete cache-key and legacy parser defects found during analysis need
  separate treatment; they are not hidden behavior changes in this refactor.

### 2026-10-14: implementation in parallel

- Initial plan committed and pushed as `acb0ea4`.
- Baseline native binary built from the immutable source archive. Detailed
  source and binary evidence remains outside Git.
- Six Sol high engineering lanes own disjoint native, local-read/index,
  composition, legacy scaffolding, mutation, and CLI files. A seventh Sol high
  lane prepares private metrics/API/performance comparison tooling.
- GitHub shows #23 merged and #24 closed without merging. The user explicitly
  chose a new PR against `master`; retain the existing worktree and branch.
- Native account projection preserves sender's four columns plus final-ID
  observation. Descriptor and recipient helpers preserve per-caller error text.
- The combined mailbox resolver query caused an indexed exact lookup to become
  a scan with temporary ordering in a fixture query plan. Retain the original
  staged exact/Archive/nested queries and share candidate policy instead.
- CLI invocation ownership is expected to add some code. Report that cost
  honestly; aggregate reduction must count the replacement code.
- Compilation during concurrent edits is transient; final validation begins
  only after every engineering lane reports stable source.

### 2026-10-14: first integrated validation

- All six initial engineering lanes report stable source. The managing agent
  inspected shared projections/decoders, composition lifecycle, local hydration,
  index preference, legacy execution/projections, invocation ownership, and
  batch/verification sequencing.
- An independent Sol high review of changed core code and a second pass over
  mutation/CLI/bulk code found no actionable source-level regression. These
  reviews do not substitute for runtime or performance validation.
- The complete native Go suite passes (`pkg/mail` 92.195 seconds); the complete
  non-cgo suite passes (`pkg/mail` 92.386 seconds); `go vet ./...` passes.
- The unchanged command surface snapshot passes. A new 48-call concurrent
  public `Run` regression passes under the race detector, using version/help/
  error paths and independent writers without live Mail.
- Source comparison includes every replacement/untracked helper: 26,327 →
  25,970 physical production Go lines; 23,236 → 22,910 meaningful lines.
  Credited reduction is **326**, with **zero exported AST contract differences**.
  This remains an intermediate snapshot before the additional audited cleanup.
- Coordinator lengths are `RunBatch` 52, `VerifyMutations` 26, and
  `composeNativeOutgoingOwned` 50. Their extracted helpers are fully counted.
- The initial 2,000-line forecast was overstated. A second source audit supports
  roughly 115 additional safe lines, including 41 newly orphaned private helper
  lines. The numerical acceptance gate is still open; the user has been asked
  whether to retain it or revise to the design goals plus maximum verified safe
  reduction. Do not claim this goal complete while that decision is pending.
- Additional proven cleanup covers private orphans, HTML inventory/rendering,
  indexed envelope conversion, recent defaults, subject whitespace, checkpoint
  membership, and equivalent cached attachment routing. Preserve all existing
  error, formatting, completeness, query, and operation-count contracts.
- Local baseline `doctor` is healthy after the legacy diagnostic launched Mail.
  Read-only benchmark manifests are prepared in private evidence.
- The baseline test CLI is staged on home-server. The user has been asked to
  enable Terminal Full Disk Access, Accessibility, and Automation to Mail and
  run read-only legacy/native diagnostics. Remote live acceptance remains pending.

### 2026-10-14: revised acceptance and final source

- The user explicitly accepted “design improvements plus maximum verified safe
  reduction” as the revised goal. The original 2,000-line forecast is withdrawn,
  and its shortfall is recorded rather than hidden by excluding helper files.
- Additional audited cleanup is integrated and its targeted tests pass.
- Frozen candidate source measures 26,327 → 25,847 physical production Go lines
  (**480 removed**) and 23,236 → 22,795 meaningful lines (**441 removed**).
  Exported AST contract differences remain **zero**. Tests, comments, blanks,
  and documentation contribute no reduction credit.
- The user reports home-server permissions enabled. Verify actual runtime
  access in the execution context before claiming remote acceptance.
- The live INBOX body preflight has 33 partial files among 100 rows. Treat these
  as a mixed-cache diagnostic, not a complete-cache workload. Use an additional
  deterministic complete-cache fixture benchmark for the 100-body fast path.
- The final full native suite is running after these additional source changes;
  offline archive/benchmark scripts pass all 18 tests.

### 2026-10-14: final offline proof

- Final native and non-cgo suites pass after the final cleanup; native
  `pkg/mail` completes in 97.755 seconds. Final vet and targeted race checks
  across CLI invocation, native descriptors, shared reads, snapshots, and index
  pooling pass. Apple Silicon and Intel builds pass; Intel runs under Rosetta.
- An identical private complete-cache fixture in baseline/candidate exercises
  the public content batch API against real embedded SQLite and 100 complete
  synthetic EMLX files. Exact body/envelope/diagnostic JSON digests match. Both
  require 100 file reads/extractions, no partial/error rows, zero Mail events or
  content requests, and zero scripting/SQLite subprocesses.
- Four-mode live timing preflight succeeds with zero failures across all eight
  primary read workloads. Diagnostic samples do not satisfy the performance
  gate; the larger paired series follows.
- Home-server Terminal index access now succeeds. Legacy version access succeeds
  but account/live probes time out; native consent remains unresolved. SSH
  access has different permission attribution. A targeted user request checks
  Terminal Automation and any open Mail alert rather than assuming permission.

### 2026-10-14: TUI parity and allocation diagnosis

- Baseline/candidate each pass the same 38 top-level / 43 total TUI test
  events under the race detector. Private model replay covers 80×24, 120×30,
  and 150×40, navigation, selection, scrolling, search, compose/reply-all,
  actual read cancellation, forced quit, and queued-write completion/failure.
  This is offline model proof, not a physical terminal/provider claim.
- All 31 representative offline CLI cases match: help, agent help, command
  inventory, version, usage/parse/config errors, and receipt guards. Plain
  bytes/exit codes match; only JSON duration is normalized.
- Ten randomized complete-cache benchmark pairs show approximately 15 extra
  fixed allocations per batch, with latency within 1.2% and the 100-body
  candidate slightly faster. Source review traces eager fallback SQL building
  on exact mailbox hits; final timings are paused while restoring lazy
  exact→Archive→broad preparation. Repeat measurements after that source change.
- A home-server process sample locates the waiting native diagnostic inside
  `AEDeterminePermissionToAutomateTarget`, before event dispatch. This is the
  frozen baseline CLI, so it does not establish a refactor regression. Remote
  live acceptance remains pending the user's targeted permission/UI check.

### 2026-10-14: performance correction integrated

- Resolver fallback construction is now lazy; verification keeps its shared
  candidate policy and single-statement snapshot. Exact-hit SQL, fallback
  ordering, query count, missing/error semantics, and projection remain intact.
  A schema-error test confirms that a failed candidate cannot advance.
- Including all performance-correction helpers, final source counts are
  26,327 → 25,866 physical lines (**461 removed**) and 23,236 → 22,810
  meaningful lines (**426 removed**), with zero exported AST differences.
  The previous 441 figure is superseded, not an additional reduction.
- The remote non-isolated test attempt was stopped after its captured stack
  proved lock contention with the waiting diagnostic. Vet/build passed. The
  diagnostic is stopped; a full rerun uses an isolated home and automation lock.
  Do not report the interrupted test attempt as passing.
- The final native suite passes after lazy preparation; final non-cgo and
  subsequent immutable builds/repeated timings follow.

### 2026-10-14: remote access recovered and final timing started

- After the user's permission/UI correction, Terminal-hosted native and legacy
  diagnostics both pass every check on macOS 27.2: Automation, account/live
  access, and local index. One authorized account is available; keep its actual
  identity and addresses private. Native live fixture acceptance is underway.
- The remote queued-execution fixture's 50 ms shell deadline fails repeatedly
  on this host. Replace only that test's timing assumption with an explicit
  release barrier: hold the process lock 500 ms against a fresh 250 ms execution
  budget, reject early completion, then release and assert success/order. Ten
  consecutive local runs pass. Production deadlines are unchanged.
- Full local native/non-cgo suites, vet, and both native builds pass after the
  lazy SQL correction. An initial non-cgo attempt included the temporary native
  cache harness and failed its intentional cgo requirement; remove temporary
  copies and use identical private overlays for that native-only measurement.
  The clean full non-cgo rerun passes.
- Ten final complete-cache pairs retain four queries, N file reads/extractions,
  zero events/content/scripting, and exact parity. Warm throughput differs by
  approximately -0.6% to +1.7%; fixed allocation overhead is reduced to roughly
  4–7 allocations per batch. These small fixture timing differences are not
  a speed-improvement claim. The 50+100 four-mode live series is now running.
- The first remote live harness stopped before sending because stored Mail
  bodies on this host include a leading line break. Compare the unsent draft
  byte-for-byte with the frozen baseline before accepting that host behavior;
  resume only the verified unsent draft, without replaying a send.

### 2026-10-14: remote setup without user intervention

- The user requested no further questions and autonomous test setup/cleanup.
  Use only scoped Mail CLI operations and private validation extensions; do
  not weaken stored-draft MIME safeguards to make a test pass.
- Main four-mode matrix completes **4,800 successful observations**, zero
  failures. Median differences are small; several native p95 tails are higher.
  Repeat those cases independently before making a no-regression conclusion.
  Native/legacy operation counts and CPU remain effectively unchanged.
- Two received physical copies on home-server share the exact same RFC
  message identity and payload. Record physical copies separately from SMTP
  identities; do not replay the acknowledged send.
- The public Mail dictionary exposes default message format (`demf`, plain
  `dmpt`, rich `dmrt`). A private CLI overlay uses the existing native transport
  and gate for reversible test setup/readback. Original rich format is restored
  and independently read back. Changing the default still produces rich
  scripted saved drafts on this host, including an empty-body probe.
- Those saved drafts remain available and fail reconstruction before dispatch.
  A private owned-draft UI conversion harness is being prepared using public
  Accessibility and exact fixture ownership; it is not a shipped command or
  source-reduction credit. All extra disposable drafts/copies require cleanup.
- The final home-server full suite and targeted race checks pass after the
  isolated environment and barrier fixture correction. Native build and vet pass.

### 2026-10-14: final repeated measurements and native attachment correction

- Final refactor matrix: 6,400 measured CLI observations, 6,399 successful and
  one index-disabled native list-1 internal error. Its exact error text was not
  retained; preserve it in the result. A diagnostic repeat with captured private
  outputs passes all 80 observations (20 native pairs each for lists 1 and 25),
  with unchanged discrete operation counts. The failure does not recur.
- Original native warm p95 increases for mailbox/list-1/list-25/detail remain
  reported. Independent 150-pair repeats have p95 differences of -11.1, +7.2,
  +11.4, and +16.3 ms; bootstrap 95% intervals all include zero. Index-disabled
  paired-median intervals also include zero. No reproducible regression is
  established; do not claim identical timings or that every original tail improved.
- Controlled complete-cache fixture: 10 process pairs, four batch sizes,
  8,000 timed operations; four queries/op, N files/extractions, zero events,
  content requests, scripting or SQLite processes. Median throughput shifts
  from -0.6% to +1.7%, with approximately 4-7 more allocations per batch.
- Final scoped review found a test cleanup ordering issue: queued goroutine
  cancellation and joining must run before PATH/lock environment restoration.
  Move the environment setup before cleanup registration. Ten local race runs
  pass; production is unchanged.
- Live native direct/reply-all/forward deliveries, bodies/envelopes, flag/read
  changes, and generic archive plus sync/0/15/30-second observations pass on
  both hosts. Received physical copies are normalized by RFC identity, retaining
  their physical IDs; acknowledged sends are never replayed.
- Mail rejects only the optional attachment MIME getter `attp` with -10000 on
  both hosts; filename/size/downloaded reads succeed. Frozen native listing and
  export fail while legacy lists MIME unknown. Limit the fix to that getter,
  leaving shared error policies, required fields, permissions, timeout and
  malformed-value failures intact. Fake tests and local/remote races pass;
  normal native reply/forward exports now match exact synthetic byte hashes.
- Source after this final interop correction: 26,327 -> 25,868 physical lines
  (459 removed), 23,236 -> 22,810 meaningful lines (426 removed), zero exported
  AST differences. Two explanatory comment lines supersede the physical 461
  figure; meaningful reduction remains 426. The corrected attachment getter
  is outside every immutable timed benchmark workload.
- Gmail five-minute archive observations and remaining stored-draft setup and
  all-fixture cleanup are in progress. Do not mark those gates complete early.

### 2026-10-14: completed local live validation and known draft boundary

- Gmail Inbox Archive passes ten source/destination scans and read-only receipt
  reconciliation observations after explicit account sync. The final pair begins
  after 300 full seconds. The exact disposable received identity stays out of
  Inbox and remains in All Mail throughout; no archive is replayed.
- Local cleanup moves twelve physical fixture copies to Trash, then completes
  two healthy no-active-copy readbacks. One initial Gmail post-sync retention
  scan misses a moved subject while the cache changes; a later read-only run
  confirms both exact subjects retained in Trash, with zero additional moves.
- Both earlier fixture subjects supplied in the screenshot are absent from the
  iCloud canonical test scope and present only in Gmail Trash. They have no
  active Inbox/Sent/All Mail/Drafts/Spam copies in the complete scoped scans.
- Saved-draft create/show and native/legacy snapshot parity pass. Rich-draft
  update and send refusal occur before reconstruction/dispatch and preserve the
  original. On macOS 27.2 all attempted plain-format fixture paths, including
  exact owned UI conversion and UI save, still yield multipart/alternative MIME.
  Do not claim successful live plain stored-draft update/send on that host.
  Preserve the existing guard; compatible plain cases keep deterministic
  native tests. No further user setup or permission questions are needed.
- Private setup attempts are bounded and recorded outside Git. Setup-specific
  deadlines, AX helpers, format changes, and source overlays are not shipped.
  Original default rich format was restored and independently read back.
- Full local native suite and vet pass again after the final optional-MIME fix.
  Both final native architectures build; targeted attachment races pass locally
  and on macOS 27.2. Remote queue race includes the corrected cleanup ordering.
- Paired bootstrap intervals for controlled complete-cache median deltas include
  zero for every size; measured allocation and timing differences stay visible.
- Remote cleanup has moved all twelve remaining active physical copies to
  Trash; final sync/readback and PR delivery/review monitoring are in progress.

### 2026-10-14: remote cleanup complete and PR prepared

- Home-server cleanup succeeds: eleven exact recorded subjects, twelve remaining
  active physical copies moved once to Deleted Messages, no missing recorded
  subjects, two complete post-sync scans over every account mailbox, zero active
  copies outside Trash. All moved and preexisting fixture copies remain retained.
- A partial creation produced a known disposable unsent draft with no recipients.
  Cleanup accepts it only under its exact recorded UUID, freshly owned sender,
  Drafts/Trash scope, zero To/Cc/Bcc, and exact recorded body; sent or received
  zero-recipient messages remain refused. Preflight refusals made no writes.
- Public close acknowledges an outgoing window close while Mail may retain its
  scripting descriptor; descriptor absence is not a window-close receipt.
  Private cleanup probing is not shipped and does not establish an absence claim.
  Stored-message cleanup above is independently verified by normal CLI scans.
- All owned production changes are committed per file. User-facing documentation
  includes independent invocation behavior; no private Mail evidence is included.
- Open the new PR against master, attach it to this task, and inspect automated
  review reactions/comments. Do not merge, release, tag, or install.

### 2026-10-14: delivered

- New PR: https://github.com/0xSMW/mail.app-cli/pull/26, targeting master;
  attached to this task. It contains aggregate source and before/after result
  tables, repeat confidence intervals, the original diagnostic failure and
  successful 80-observation retest, live outcomes, and the explicit stored-draft
  boundary. No raw private Mail evidence is included.
- User-facing improvements: docs/3.0.0-improvements.md. Final source reduction
  is 426 meaningful / 459 physical production lines against frozen 3.0, with
  zero exported AST contract differences. Coordinators are 52/26/50 lines.
- Local and remote normal-CLI cleanup receipts and complete post-sync scans
  confirm no active fixture copies. Deleted Messages/Trash retains the copies.
  The two earlier screenshot fixtures are also retained only in Gmail Trash.
- Final native full suite, vet, targeted races, non-cgo suite, surface/installer,
  offline scripts, CLI contracts, TUI replay, and both native builds pass.
  Detailed remote evidence is copied to the private local validation directory
  with archive integrity checked. Installed daily-driver binaries are unchanged.
- PR inspected after creation and after the documentation push: open and
  mergeable, no automated reviews, findings, or CI checks posted at either
  inspection. Review completion is not claimed. The accepted implementation,
  validation, documentation, cleanup, and PR-delivery goal is complete.
- No merge, release, tag, or install was performed.

### 2026-10-15: explicit Gmail labels and daily-driver installation

The user authorized a focused Gmail label adapter, an update to PR #26,
validation, installation of 3.0.0 on PATH, and an exact Important-removal
operation after validation. The Daily Mail heartbeat remains untouched.

- [x] Add a narrow Gmail REST adapter with account-bound OAuth credentials in
  Keychain, metadata identity resolution, and single-message label deltas.
- [x] Add explicit label list/show/add/remove, dry-run, durable receipts, and
  read-only reconciliation; distinguish provider confirmation from Mail's local
  convergence. Preserve existing archive behavior and system-action boundaries.
- [x] Run focused identity, idempotency, failed-verification, receipt, auth, and
  archive regression checks, then native/non-cgo suites and vet.
- [x] Validate live label operations and compare existing read workloads with
  the frozen 3.0 baseline. Keep private messages and credentials outside Git.
- [x] Install the verified 3.0.0 candidate on the canonical PATH, retain a
  recoverable prior binary, and verify the resolved binary and native doctor.
- [x] Apply the authorized exact-message label removal and verify fresh Gmail
  and Mail membership/read state, then push per-file commits and update PR #26.

Implementation is split among 6.1 Sol high transport/auth, workflow, and CLI
agents. The managing agent owns integration, live operations, installation,
final verification, and delivery. The label adapter does not alter Mail's
archive policy or infer actual Smart Mailbox visibility from label changes.

The aggregate adapter results and timing table are in
[Gmail label validation](gmail-labels-validation.md). The native and non-cgo
full suites pass; the focused adapter and CLI checks also pass on macOS 27.2.
Live archive retains Important and a custom label, while explicit label removal
and idempotent no-ops confirm provider state and eventual Mail convergence.

Delivery complete: PR #26 includes the adapter and aggregate results; canonical
PATH now resolves the signed native 3.0.0 CLI with a recoverable 2.4.0 backup.
The authorized production change is confirmed by both provider readback and a
complete Mail scan, preserving archive/read state and unrelated labels. A
canceled Keychain prompt stopped a preflight before any production mutation;
stable signing, versioned credential ownership, one load per address, and
noninteractive reads remove that repeated-prompt path. The heartbeat is untouched.
