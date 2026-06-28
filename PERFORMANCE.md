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

## Remaining Constraints

- `--with-content` still requires Mail.app/JXA because the Envelope Index does
  not contain full message bodies or recipient expansion.
- Provider-specific mailbox storage can differ from visible folder membership;
  index-backed reads must preserve label membership rather than relying only on
  the message storage mailbox.
- Mail.app and Spotlight indexing can lag briefly behind server state, so index
  paths should keep JXA fallbacks for unsupported or unresolved cases.
- First-run timings may be noisier because Mail.app, SQLite pages, and system
  caches may be cold.

## Recommendations

- Prefer metadata-only list/search commands for interactive workflows.
- Use `--with-content` only when message bodies are needed.
- Keep cache enabled for repeated automation over the same mailbox/query.
- Prefer narrow account or mailbox filters when searching large mail stores.
