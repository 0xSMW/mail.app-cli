# Performance Characteristics

## Mail.app API Limitations

Mail.app's AppleScript/JXA bridge is the main performance ceiling. Calling
`mailbox.messages()` can materialize an entire mailbox before the CLI can slice
or filter results, and every `osascript` invocation has process-start and Apple
Events overhead.

The CLI now avoids that path where it can by reading Mail's local Envelope Index
for metadata-only operations, then falling back to JXA when full message content
or unsupported mailbox shapes require it.

## Current Optimizations

- Envelope Index reads for mailbox counts and metadata-only message lists.
- Label-aware index queries so folder membership is preserved for providers that
  store messages in an archive-style backing mailbox.
- Direct `messages.byId(...)` lookup for message details, with fallback to the
  previous full-ID scan.
- Default search targets account inboxes directly instead of discovering every
  mailbox first.
- Bounded Mail/JXA fan-out to reduce Mail.app and Apple Events contention.
- Per-command cache setup only when caching is enabled.
- Per-client account caching within a single CLI invocation.
- Top-N sorting for paged unified views.
- Explicit multi-mailbox scans amortize account discovery and command overhead,
  retaining coverage and matching mailbox membership instead of repeating rows.
- Selected-body reads reuse bounded serial bridge sessions and preserve per-ID
  results across failures; compatible mutation batches reuse the same bridge
  while retaining Go-side durable journaling between each phase.
- Selected-body reads and `show` take the body from Mail.app's message file on
  disk. Mail.app answers only the metadata request, so a body that stalls
  Mail.app's renderer no longer blocks the read or the commands after it.
- Mailbox lookup reads each level's names in one Apple Event and descends only
  into mailboxes that have children. It picks the same mailbox as the previous
  one-mailbox-at-a-time walk, which remains the fallback.
- Scan pages by receipt time and local ID instead of an offset, so each page
  costs the same wherever it sits in the mailbox.
- Mark/flag verification stops polling confirmed IDs. Delete and Gmail archive
  still use the full settling window to guard against regenerated message IDs.

## Before/After Benchmarks

Benchmarks were run against local Mail data with output redirected away from the
terminal. Labels and account names are intentionally redacted. Values are median
wall-clock times from three runs.

| Command class | Before | After | Improvement | Speedup |
|---|---:|---:|---:|---:|
| Account listing, uncached | 0.113s | 0.106s | 5.4% | 1.1x |
| Mailbox listing, uncached | 1.265s | 0.119s | 90.6% | 10.6x |
| Large metadata-only message list | 0.126s | 0.123s | 2.4% | 1.0x |
| Labeled-folder metadata-only message list | 0.353s | 0.128s | 63.7% | 2.8x |
| Single message detail lookup | 1.717s | 0.400s | 76.7% | 4.3x |
| Account-scoped search | 1.872s | 0.149s | 92.0% | 12.6x |
| Unified inbox-style view | 0.695s | 0.481s | 30.9% | 1.4x |
| Unified unread view | 0.787s | 0.148s | 81.2% | 5.3x |

Representative output checks confirmed that mailbox names, message IDs, search
IDs, and message detail content matched between the old and optimized paths for
the benchmarked cases.

### Version 2.3.0

Single runs against local Mail data, serial, 2.2.1 compared with 2.3.0.

| Command class | 2.2.1 | 2.3.0 | Speedup |
|---|---:|---:|---:|
| Selected-body read, 30 messages, one account | 105.0s, 2 failed | 2.8s, none failed | 38x |
| Selected-body read, 138 messages, three accounts | 279.1s, 7 failed | 13.9s, none failed | 20x |
| Flag then unflag one message, verified | 3.21s | 0.76s | 4.2x |
| Batch flag then unflag two messages, verified | 2.65s | 1.14s | 2.3x |
| Mailbox lookup, last of about 400 mailboxes | 1.38s | 0.04s | 31x |
| Scan page of 5,000 from a 110,000-message mailbox | not possible past the first page | 0.08s to 0.24s | |

Of 152 bodies that both versions returned, 140 matched after whitespace was
normalized. In 6 of the other 12, Mail.app had returned an empty body and the
disk read returned the text. The remaining 6 differ where a stylesheet changes
the layout. Metadata fields matched in every message.

## Remaining Constraints

- `list --with-content` still asks Mail.app to render each body. `read` and
  `show` do not.
- The disk body is a text conversion of the HTML part. It applies inline
  styles only, so a layout set by a stylesheet class can differ from
  Mail.app's rendering in spacing or in which hidden text appears.
- Provider-specific mailbox storage can differ from visible folder membership;
  index-backed reads must preserve label membership rather than relying only on
  the message storage mailbox.
- Mail.app and Spotlight indexing can lag briefly behind server state, so index
  paths should keep JXA fallbacks for unsupported or unresolved cases.
- First-run timings may be noisier because Mail.app, SQLite pages, and system
  caches may be cold.

## Recommendations

- Prefer metadata-only list/search commands for interactive workflows.
- Read bodies with `messages read` or `show`. Use `list --with-content` only
  when neither fits.
- Keep cache enabled for browsing; use live scans or `--no-cache` lists for
  post-mutation completion checks.
- Prefer narrow account or mailbox filters when searching large mail stores.
