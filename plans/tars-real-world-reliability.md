# Mail.app CLI Real-World Reliability Plan

This plan tracks issues observed in repeated TARS mail workflows. The implementation is divided into bounded releases so safety-critical reliability work lands before broader command-surface expansion.

## Scope and invariants

- Serialize all Mail.app automation within one process.
- Bound every AppleScript and JXA subprocess and terminate its process group on timeout.
- Preserve an explicit unknown outcome when a timed-out mutation may have applied.
- Never treat partial search results as complete without an explicit opt-in.
- Verify mutations against fresh source and destination state.
- Keep JSON output stable, versioned, and machine-readable.
- Preserve compatibility unless a documented safety issue requires a breaking change.

## Release 1: bridge reliability and diagnostics

### R1.1 Global Mail.app execution gate

- [x] Route every AppleScript and JXA invocation through process-wide and cross-process serialization gates.
- [x] Ensure nested helpers cannot deadlock by acquiring the gate more than once.
- [x] Add concurrency tests proving goroutines and separate CLI processes execute serially.
- [x] Neutralize internal Mail.app fan-out at the automation boundary.

Acceptance criteria:

- At most one `osascript` subprocess is active per `mail-app-cli` process.
- Existing command behavior remains unchanged for successful serial calls.
- Race-enabled tests pass.

### R1.2 Universal timeouts and child cleanup

- [x] Give AppleScript and JXA operations bounded default deadlines.
- [ ] Allow safe configuration through an internal option or documented environment variable.
- [x] Start automation subprocesses in a process group.
- [x] Terminate the full process group on timeout and wait for cleanup.
- [x] Return typed timeout errors distinct from Mail.app-unavailable and script failures.
- [x] Add tests for timeout classification, process serialization, and lock-wait cancellation.

Acceptance criteria:

- No automation path can wait indefinitely.
- Timeout errors include operation type and duration without leaking script contents.
- Timed-out subprocesses do not remain running.

### R1.3 Honest health diagnostics

- [x] Extend `doctor` to report binary version, Mail.app bridge availability, automation access, account access, Envelope Index availability, and a bounded harmless live probe independently.
- [x] Keep partial diagnostic results when one probe fails.
- [x] Make overall health false when live Mail access is unavailable.
- [x] Add stable JSON fields and tests.

Acceptance criteria:

- Envelope Index availability cannot produce an overall healthy result when the Mail bridge fails.
- Each failed layer has a specific remediation-oriented error category.

### R1.4 Explicit partial-search contract

- [x] Return per-mailbox search failures in structured output.
- [x] Include `complete`, `searchedMailboxes`, and `failedMailboxes` metadata.
- [x] Add `--allow-partial` for workflows that intentionally accept incomplete results.
- [x] Fail closed by default when cross-mailbox search is incomplete.
- [x] Preserve a compatibility path for callers expecting the current result array.

Acceptance criteria:

- Duplicate-prevention and exact-once workflows can distinguish zero matches from incomplete search.
- A mailbox timeout cannot silently appear as a complete empty result.

## Release 2: mutation integrity

### R2.1 Unified mutation receipts

- [ ] Define shared mutation states: `applied_verified`, `already_applied`, `failed`, `unknown_after_timeout`, `source_still_present`, and `destination_unverified`.
- [ ] Use the same receipt shape for single and batch mutations.
- [ ] Add `--dry-run` parity to single archive, move, and delete commands.
- [ ] Preserve per-item errors and produce a non-zero exit for failed or unknown results.

### R2.2 Fresh two-sided verification

- [ ] Re-list or resolve each selected message immediately before mutation.
- [ ] Verify source absence after archive, move, and delete.
- [ ] Verify destination presence after archive and move.
- [ ] Handle provider behavior that regenerates a source-label copy.
- [ ] Treat already-absent messages as idempotent success only when absence is the requested end state.

### R2.3 Stable message identity

- [ ] Add RFC `Message-ID`, `In-Reply-To`, and `References` when available.
- [ ] Include normalized To, CC, and BCC recipients.
- [ ] Carry account and mailbox identity with every message reference.
- [ ] Resolve current Mail IDs from stable identity before mutation.
- [ ] Mark synthetic thread keys explicitly.

## Release 3: safe composition

### R3.1 Idempotent verified send

- [ ] Add an operation/idempotency key.
- [ ] Add identical-Sent preflight support.
- [ ] Add `--verify`, `--wait-until-visible`, and `--reject-existing`.
- [ ] Return the verified Sent message identity.
- [ ] Preserve `unknown_after_timeout` when delivery state cannot be established.

### R3.2 Reply and reply-all

- [ ] Add first-class `reply` and `reply-all` commands.
- [ ] Preserve thread headers, original non-self recipients, and To/CC placement.
- [ ] Support dry-run recipient/thread previews.
- [ ] Verify sender account, thread linkage, recipients, subject, body, and attachments in Sent Mail.

### R3.3 Draft reconciliation

- [ ] Replace fixed sleeps and subject/body matching with a correlation marker and bounded polling.
- [ ] Return durable draft identity or an explicit unresolved state.
- [ ] Reconcile unresolved creation before allowing retries.
- [ ] Support recipient and attachment changes during draft updates.
- [ ] Verify replacement before deleting the prior draft.

### R3.4 Attachment safety

- [ ] Validate every attachment before send.
- [ ] Abort the send if any requested attachment cannot be attached.
- [ ] Verify Sent attachment names, sizes, and counts.
- [ ] Verify saved attachment size and optionally SHA-256.
- [ ] Infer MIME type from trusted metadata or extension when Mail reports unknown.

## Release 4: search and automation ergonomics

### R4.1 Recipient-aware and exact search

- [ ] Add `--recipient`, `--to`, `--cc`, `--bcc`, and exact-match modes.
- [ ] Permit filter-only searches without a dummy positional query.
- [ ] Add recipient fields to list, Sent, thread, and draft summaries.

### R4.2 Stable JSON schema

- [ ] Choose and document one field-casing convention.
- [ ] Add `schemaVersion` to structured envelopes.
- [ ] Prevent silent null projections through documented schema inspection or strict query support.
- [ ] Preserve compatibility aliases during migration.

### R4.3 Consistent selectors and live-read semantics

- [ ] Normalize `--since`, `--until`, unread, flagged, limit, offset, and live/cache flags across list aliases and batch commands.
- [ ] Add explicit live-read behavior and cache provenance metadata.
- [ ] Invalidate derived mailbox views after mutations.

### R4.4 Bounded content and triage

- [ ] Add metadata-only message display.
- [ ] Add body byte/line caps with explicit truncation metadata.
- [ ] Add a bounded inventory/triage command returning snippets, recipients, attachment summaries, and mailbox provenance.

## Verification gates

- [x] Unit tests for each changed package.
- [x] `go test ./...`.
- [x] `go test -race ./...` for synchronization changes.
- [x] Focused CLI contract tests for diagnostics and partial-search behavior.
- [x] Adversarial review restricted to this branch's intended diff.
- [x] Review findings fixed and re-reviewed before commit.
- [x] Final diff checked to exclude pre-existing unrelated work.

## Current implementation tranche

This branch targets Release 1 only. Later releases remain tracked here and should land in separately reviewable pull requests.
