# Mail 3.0 refactor: living implementation plan

## Objective

Remove at least 2,000 net meaningful production Go implementation lines and
organize the core around explicit read, composition, and mutation workflows.
Preserve every exported API, CLI/TUI interaction, capability, output and error
contract, reliability guarantee, and performance characteristic.

This file is the working record for progress, todos, decisions, findings, and
validation. Update it after each integrated stage and whenever a material
constraint or decision changes. An unchecked acceptance gate remains required
work. Line-count targets never justify weaker guards or denser formatting.

## Baseline and delivery

- Baseline commit: `8271f9b869cd2ad2e7b98454607387b4c52e3fc2`.
- Working branch: `0xSMW/mail-native-v3`, in its existing managed worktree.
- Delivery: update existing PR #24, with per-file commits and aggregate results.
- Version: retain 3.0.0 and output schema version 1.
- Production Go baseline: 26,327 formatted physical lines, including embedded
  scripts. `pkg/mail`: 15,640 lines and 518 functions.
- Largest coordinators: `RunBatch` 378 lines, `VerifyMutations` 271 lines,
  `composeNativeOutgoingOwned` 248 lines. Aim for roughly 100 lines or fewer per
  coordinator, with explicit domain responsibilities in the extracted steps.
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

- [ ] Share descriptor list/string/integer/boolean/reference decoding where
      contracts match, preserving contextual errors.
- [ ] Share bulk account projection, identity/order brackets, and duplicate-ID
      checks between discovery and sender selection.
- [ ] Keep sender selection pure after each fresh projected observation.
- [ ] Consolidate recipient and attachment projections without adding getters.
- [ ] Preserve operation-specific Apple error-code policies exactly.
- [ ] Remove only proven unused private paths; retain exported entry points.

### Read workflows, MIME, and index policy

- [ ] Share single-message and batch MIME enrichment and diagnostics.
- [ ] Share mailbox selection preference and membership SQL while preserving
      physical ownership versus label membership.
- [ ] Share index fallback eligibility and warning policy where equivalent.
- [ ] Retain the single-statement source/destination verification snapshot.
- [ ] Preserve streaming, size/depth limits, charset behavior, diagnostics,
      partial-file handling, and the stricter stored-draft reconstruction policy.
- [ ] Keep read concurrency, query pooling, and local-content fast paths intact.

### Mutation orchestration and receipts

- [ ] Introduce an invocation-owned batch runner with explicit preparation,
      dispatch, recording, verification, and completion responsibilities.
- [ ] Consolidate repeated status/counter/journal bookkeeping.
- [ ] Make accepted, uncertain, verified, and cleanup states distinguishable.
- [ ] Simplify verification sequencing without changing observation schedules,
      early completion rules, final-observation semantics, or sync behavior.
- [ ] Retarget tests of obsolete private algorithms onto production workflows
      before removing those algorithms.

### Composition and draft preparation

- [ ] Share outgoing input validation across ordinary and threaded composition.
- [ ] Share prepared-draft data and equivalent replacement preparation.
- [ ] Simplify the native composition coordinator into named responsibilities.
- [ ] Preserve each backend's current rich-draft, whitespace, recipient, sender,
      attachment, cleanup, and result contracts.
- [ ] Keep atomic compare-and-delete inside the legacy bridge where required.

### Legacy scaffolding and CLI ownership

- [ ] Consolidate script execution, JSON decoding, status parsing, and recurring
      message projections without changing script process or Mail-event counts.
- [ ] Keep strict-ID, bounded-predicate, ordinary-detail, and absence-proof
      lookup contracts distinct.
- [ ] Replace mutable global invocation ownership with local application/options
      state and fresh command construction, preserving repeated `Run` behavior.
- [ ] Share equivalent flags, mailbox-name tables, cache-loading glue, and
      presentation inputs while retaining per-command defaults and validation.
- [ ] Preserve TUI request lanes, write queue, optimistic state, selection,
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
- [ ] Build immutable baseline binaries and capture initial contracts/metrics.
- [ ] Integrate native primitives and local read/index consolidation.
- [ ] Integrate legacy scaffolding and composition preparation consolidation.
- [ ] Integrate mutation workflow and receipt simplification.
- [ ] Integrate CLI ownership and equivalent presentation-input cleanup.
- [ ] Inspect integrated diff and measure actual meaningful reduction.
- [ ] Resolve scoped independent review findings.
- [ ] Complete final validation, commit/push owned changes, update PR results,
      and inspect review status.

## Acceptance and validation

### Offline behavior and failure parity

- [ ] Exported API compatibility, command surface snapshot, and representative
      JSON/plain/help/error/exit-code contracts match the frozen baseline.
- [ ] Reads, search completeness, pagination, scan checkpoints, rules,
      signatures, attachments, drafts, threading, mutations, and reconciliation
      retain their capabilities and backend-specific semantics.
- [ ] Permission failures, cancellation before/after dispatch, lost replies,
      stale/reordered IDs, incomplete MIME, corrupt attachment bytes, and journal
      failures preserve results and actual mutation attempt counts.
- [ ] Full Go suite, vet, targeted race checks, non-cgo compatibility, installer,
      surface, and offline validation-script checks pass.
- [ ] Native Apple Silicon and Intel builds pass.

### Performance

- [ ] Compare frozen 3.0 native to refactored native and frozen 3.0 legacy to
      refactored legacy, with randomized paired observations.
- [ ] Reuse accounts, mailboxes, lists, cached detail/body batches, scoped/broad
      search, and index-unavailable workloads. Keep initial and warmed series
      distinct; Mail remains running during fresh CLI-process observations.
- [ ] Compare p50/p95 wall time, CPU, memory, native events, SQLite queries,
      local-file extraction, and subprocess counts. Investigate variation with
      repeated controlled samples; any reproducible regression blocks completion.
- [ ] Complete cached bodies retain zero Mail content requests. Native commands
      retain zero scripting/SQLite subprocesses. Batching and fresh guard event
      counts do not increase.

### TUI and live acceptance

- [ ] Replay navigation, selection, scrolling, search, compose/reply-all,
      optimistic mutations, delayed refresh, cancellation, and queued-write quit
      behavior at fixed terminal sizes.
- [ ] Verify disposable draft/save/send/reply/forward/attachment/flag/move/archive
      behavior and fixture cleanup locally.
- [ ] Run home-server macOS 27 build/fixture checks.
- [ ] Request user action for home-server Automation and Full Disk Access when
      the staged test binary is ready; then run live remote acceptance.

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
