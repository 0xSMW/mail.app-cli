# Native Mail 3.0.0 validation

## Decision

Adopt typed public Apple Events for Mail-owned changes and embedded read-only SQLite/MIME for local reads. Keep the explicit legacy backend. The primary read workloads improve, cached bodies avoid Mail rendering, and mutation verification remains strict. Do not introduce private maild repositories or a second Mail.framework persistence stack.

## Method

The source baseline reports 2.4.0 and is commit `fd0fff4853157718a85e9891a4d9841adea45d78`. The candidate reports 3.0.0. Both immutable Apple Silicon binaries were measured on the same macOS host, with randomized paired baseline, native, and candidate-legacy observations. The Intel native build also ran successfully under Rosetta. The installed CLI was preserved. After the timed series, an isolated rules-list correction handles Mail’s explicit missing-destination descriptor and dictionary labels. Source hashes confirm that every measured read path remains unchanged; the correction receives separate live and regression validation.

Primary workloads use 50 initial and 100 subsequent warmed-Mail observations per backend. Every observation starts a new CLI process; Mail remains running, so these are not Mail cold-start measurements. Body batches use 3 initial and 5 warmed observations; index-unavailable lists use 10 initial and 20 warmed observations. Those smaller series are diagnostic medians, not robust tail claims. Wall time, child-inclusive CPU, and `wait4` peak RSS were captured; the memory figure is the reported process/child high-water mark rather than summed concurrent memory. Message output was checked and discarded from aggregate performance reports. No builds, tests, or live mutations ran concurrently with the timed series.

All 3,900 comparative observations returned successful JSON envelopes. All native read samples launched zero scripting and SQLite subprocesses. Primary, complete-body, and index-unavailable workloads requested zero Mail content rendering; the cross-mailbox diagnostic records its bounded content-predicate fallback separately. Native content accounting is backed by the read routes using only envelope properties or complete local MIME, with no message all-properties getter.

## Primary warmed-Mail results

| Workload | Before p50 ms | Native p50 ms | Before p95 ms | Native p95 ms | p95 reduction |
| --- | ---: | ---: | ---: | ---: | ---: |
| Accounts | 170.8 | 132.4 | 188.6 | 145.8 | 22.7% |
| Mailboxes | 187.6 | 91.4 | 216.4 | 110.7 | 48.9% |
| List 1 message | 189.6 | 87.1 | 204.3 | 97.4 | 52.3% |
| List 10 messages | 189.9 | 90.7 | 207.0 | 106.7 | 48.5% |
| List 25 messages | 196.0 | 88.7 | 232.7 | 105.6 | 54.6% |
| List 100 messages | 198.5 | 92.7 | 230.0 | 106.0 | 53.9% |
| Complete cached detail | 396.6 | 131.1 | 458.4 | 150.0 | 67.3% |
| Scoped search | 415.0 | 270.2 | 496.0 | 327.9 | 33.9% |

All primary workloads are faster. 14 of 16 initial/warmed p95 comparisons exceed the 25% improvement target. The account-read comparisons below 25% remain target misses, even though their latency improves. None of the already-local lists regresses. Scoped search has higher reported peak RSS than the baseline in exchange for lower latency and CPU; memory does not improve uniformly.

## Complete cached body batches

| Bodies | Before median ms | Native median ms | Speedup |
| --- | ---: | ---: | ---: |
| 1 | 388.3 | 128.4 | 3.0x |
| 10 | 786.8 | 133.9 | 5.9x |
| 25 | 1783.8 | 132.5 | 13.5x |
| 100 | 6552.8 | 193.4 | 33.9x |

For 100 bodies, median CPU falls from 2988.9 to 94.9 ms; reported peak RSS falls from 43.4 to 26.4 MiB. Every native sample extracts exactly the requested complete local files, with no partial bodies or extraction errors. An additional paired 100-message read verifies equal bodies and stable metadata; timestamps compare as equivalent ISO instants. Account/mailbox/list scope comparisons also pass.

## Index-unavailable fallback

| Rows | Before median ms | Native median ms | Median change | Before CPU ms | Native CPU ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 440.7 | 514.1 | +16.6% | 145.9 | 48.3 |
| 25 | 583.5 | 671.4 | +15.1% | 192.6 | 77.2 |

Fallback median wall time remains higher, while CPU and reported memory are lower. Metadata reads batch envelope columns, reuse filters, and fall back to exact IDs when necessary. Bulk metadata projection is capped at 16,384 messages. Full ID/order checks bracket metadata and filters, including empty pages. Permission, timeout, malformed-data, and missing-object failures do not replay through scripting.

## Mutation effectiveness

- Native direct submission, reply, reply-all, and forward each produce exactly one Sent record and one self-addressed receipt. Sender/recipient values, Unicode bodies, applicable reply headers, canonical-equivalent filenames, and SHA-256 attachment bytes are verified. Release-candidate runs repeat direct delivery and sender-alias validation successfully.
- Ten randomized paired draft saves verify saved identity, content, recipients, filenames, and bytes. Native draft verification exports attachments without scripting. Same-size corrupted bytes fail the injected native regression fixture.
- Both baseline and native archive acceptance keep the source absent and the archive destination present after sync through the full five-minute observation window. No fixture is deleted to satisfy archive confirmation. These are Mail-local observations, not provider-authoritative synchronization completion.
- Read/unread, flag/unflag, and rule create/enable/disable/delete succeed and verify. Existing rules also list successfully; explicit missing move destinations remain visible rather than aborting the list. A generic Gmail move adds a label while source membership remains; both backends report that condition accurately. Gmail Inbox archive therefore uses Mail’s Archive action.
- Stored rich/special-header drafts remain blocked before submission and retain their originals. The native reconstructible-plain-draft chain passes injected success, lost-reply/no-replay, and changed-original/cleanup-pending fixtures. A positive live send of a rich stored draft is not claimed.
- Uncertain sends are dispatched at most once; receipt reconciliation does not repeat a mutation. Native failures never trigger an automatic legacy write.

## Cross-mailbox search diagnostic

Across hundreds of nonempty mailbox scopes, median wall time is 8120.2 ms before and 6145.5 ms native (24.3% lower). This uses 3 initial and 5 warmed samples per backend. Results retain identical requested account/message scope. Reported peak RSS is 23.1 MiB before and 61.2 MiB native; broad-search concurrency has a memory cost. The reader pool opens at most four read-only connections and allows independent queries to overlap; account snapshots are reused within a named search without poisoning global discovery. Index-unresolved mailbox search can request a Mail content predicate; it is not described as a complete-local-MIME workload.

## Mutation latency

Five draft-save pairs have medians of 1460.9 ms before and 1526.5 ms native (+4.5%). Write latency is broadly similar; this small sample does not establish a write-speed improvement. Verification and archive settling windows are preserved.

## Checks and cleanup

The full Go suite, vet, targeted race tests, non-cgo compatibility suite, surface snapshot, installer tests, and offline archive/benchmark script tests pass. Apple Silicon and Intel native builds report 3.0.0. The scoped independent source review has no remaining findings.

Cleanup moves 78 distinct disposable message targets to Trash, covering this run and the two supplied pre-existing archive subjects. Fresh searches after synchronization find zero active fixture messages. The temporary test rule is absent; temporary archive viewers are closed. No permanent email deletion is performed. Private account/message evidence and binary artifacts remain outside Git.
