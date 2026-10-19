# Native Mail architecture for 3.0.0

## Decision

Keep Go command handling, the output contract, durable mutation receipts, and Mail's ownership of changes. Use an embedded Objective-C bridge to send typed public Apple Events. Use embedded SQLite in read-only mode for the Envelope Index and parse complete local MIME for message bodies, recipients, and attachments. Keep scripting available through an explicit compatibility backend.

The CLI does not connect to private maild repositories or initialize Mail.framework's separate persistence stack. Native failures do not trigger a second write through scripting. Gmail's Inbox archive continues to use Mail's Archive action with public Accessibility APIs and a strictly owned temporary viewer.

## Boundaries

- The embedded Apple Event engine handles descriptors, object references, permission checks, bounded dispatch, native errors, and attempt metadata. Public dictionary codes define operations.
- Each Mail transaction owns both process-local and cross-process automation locks through resolution, dispatch, readback, and bounded cleanup. Local SQLite/MIME reads do not acquire the Mail automation gate.
- SQLite lazily opens at most four read-only readers per active path pool, reusing idle readers for serial requests. Each query owns one exclusive lease, authorizes SELECT observations only, and releases its short snapshot before reuse. Cancellation combines an interrupt with a VM progress flag so a cancellation before the first step remains effective; the watcher joins before the flag is freed or the reader is returned. Cached readers see newly committed data.
- Complete MIME sources require exact account, mailbox membership, and physical owner validation. Partial files and unavailable payloads retain diagnostics and use exact-scoped Mail reads where supported. Access and schema failures remain errors. Named cross-mailbox searches use an invocation-local account snapshot; it never masquerades as a complete global account cache or changes fresh sender guards.
- Attachment saves publish exclusively from owned staging. A cached payload and a Mail-downloaded payload receive the same overwrite protection.
- Mail owns sender selection, native composition, reply/forward relationships, message moves, flags, rules, and synchronization requests. Submission acceptance remains distinct from delivery; a sync request remains distinct from completed server synchronization.
- Stored drafts retain strict reconstructibility checks and guarded original cleanup. Rich or special-header drafts that cannot be reconstructed safely remain unchanged.

## Compatibility

The Go module is `github.com/0xSMW/mail.app-cli/v3`. The CLI reports version `3.0.0`; additive fields retain output schema version 1. macOS builds with cgo select native by default. `--backend legacy` selects scripting explicitly. `--backend auto` selects native when compiled; builds without cgo use legacy. No uncertain mutation is replayed automatically.

Native reply and forward creation is exposed in the TUI and through `send` / `drafts create` with `--reply-to`, `--reply-all`, and `--forward-of`. CLI threading flags require an exact mailbox and complete outgoing content. The ordinary legacy TUI composition path remains available.

## Validation protocol

Freeze the installed binary and the exact source baseline before changing code. Preserve their hashes and version output in a private evidence directory outside Git. Build the candidate separately. Use randomized paired observations against identical Mail state; separate fresh CLI processes from a warmed Mail application. Do not label a warmed process series as a Mail cold-start measurement.

Read workloads cover live accounts, mailbox metadata, index-backed lists of 1/10/25/100 messages, scoped search, complete cached details, attachments, and an index-unavailable fallback. Compare result scope and stable fields; native MIME enrichment may add previously absent recipient/RFC metadata. Record p50/p95 wall time, CPU, peak memory, native event counts/time, scripting process counts, SQLite queries/connections, and MIME extraction counts. Keep message output out of aggregate performance reports.

`scripts/benchmark-native.py` accepts a private JSON manifest of read-only workloads and writes an aggregate report with measurements, without message content. An example workload is:

```json
[{"name":"list-25","args":["messages","list","--account","Example Account","--mailbox","INBOX","--limit","25","--no-cache","--json"]}]
```

Use 50 fresh-process and 100 warmed-Mail samples per primary read workload. Target at least 25% lower p95 for the affected bridge operations and no more than 10% regression for already-local lists. Complete cached bodies must cause zero Mail content requests, and ordinary native commands must launch zero scripting processes. Compare confirmed mutation latency separately from active transport time; retain the same verification and settling windows.

Live mutations use only user-authorized disposable messages and an address controlled by the user. Verify sender/recipient readback, Unicode bodies, saved-draft identity, filenames and SHA-256 bytes, exactly one Sent record and self-addressed receipt, reply headers, flags, moves, and cleanup. Archive checks preserve source-removal and destination-membership evidence after sync through the full five-minute settling window. Do not delete a recurring fixture to make archive confirmation pass.

Fault fixtures cover predispatch denial, timeout/cancellation after dispatch, lost replies, changed source objects, ambiguous identities, delayed local state, partial MIME, attachment races, and cleanup failure. Every send is dispatched at most once. Unknown results remain uncertain. Reconciliation remains read-only and does not sync or repeat mutations. TUI tests retain read cancellation, serialized writes, immutable reply identity, and settled-state behavior.

Run the complete Go suite, targeted race tests, non-cgo compatibility tests, command-surface snapshot, installer tests, and offline archive-check tests. Validate native builds for Apple Silicon and Intel. Inspect the actual diff and live receipts before opening a regular PR. Public release text contains user-facing changes only; private inbox evidence stays outside the repository.
