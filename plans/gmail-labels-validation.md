# Gmail label adapter validation

The adapter adds explicit Gmail message-label membership operations to 3.0.0.
Existing Mail archive, move, read, flag, composition, and send implementations
remain unchanged. Raw mail evidence, OAuth credentials, and fixture identities
are kept outside Git.

## Effectiveness

| Check | Result |
| --- | --- |
| Authorized credential import and Keychain reuse | Pass; fresh provider profile matches the selected Mail account |
| Label catalog and exact message inspection | Pass; canonical IDs and complete user-label names retained |
| Dry-run | Pass; provider and Mail states read without a label mutation |
| Important and custom-label add/remove | Pass; fresh provider reads confirm exact label deltas |
| Repeated add/remove | Pass; verified no-op with a durable receipt, no mutation dispatch |
| Unsupported system labels | Pass; capability error before dispatch, with existing-command guidance |
| Gmail archive regression | Pass live; INBOX removed while Important, custom label, Sent, and read state remain |
| Read-only reconciliation | Pass; fresh provider/Mail reads, no mutation or implicit sync |
| Fixture retention | Pass; two local physical copies retained read in All Mail, absent from Inbox, Important, test custom label, Spam, and Trash |
| Delayed Mail reflection | Observed live; provider confirmation remains distinct from local pending state; fresh no-ops confirm final Mail convergence |

Receipts intentionally retain their own baseline. A later deliberate label
change can prevent an older receipt's complete-baseline comparison from matching,
even while a fresh observation correctly reports the current desired state.

## Existing read efficiency

Compare frozen pre-adapter 3.0.0 against the adapter candidate using randomized
paired fresh CLI processes. Each workload has one excluded warmup pair and ten
measured pairs. Mail stays running. Accounts, mailboxes, and message lists bypass
the CLI cache; detail follows its normal content path. These 80 measured process
observations are diagnostics, not a high-confidence tail benchmark.

| Workload | Baseline median | Candidate median | Change |
| --- | ---: | ---: | ---: |
| Accounts, live | 115.26 ms | 119.77 ms | +3.91% |
| Mailboxes, live | 84.30 ms | 83.87 ms | -0.51% |
| List 25, live | 82.76 ms | 84.65 ms | +2.28% |
| Message detail | 469.71 ms | 469.52 ms | -0.04% |

All 30 count-bearing pairs have equal returned counts. Detail is not a list.
A separate 80-observation cache series records median startup increases of
0.61 ms for accounts, 0.48 ms for mailboxes, and 0.35 ms for list 25; detail
changes by -2.19 ms. The new command registration has a small startup cost;
existing reads perform no Gmail HTTP or Keychain work.

Before this adapter, a mailbox move could acknowledge a request while Important
remained. There was no successful explicit-label baseline. Live fixture label
changes now complete with fresh provider verification in 5.12-5.97 seconds;
no-op observations take 3.87-5.51 seconds. Each actual change performs one
modification and one independent post-modification provider read. Account,
catalog, identity, and baseline reads are additional preflight work. Provider
confirmation time and Mail convergence time remain separate measurements.

## Repository and platform checks

- Full native and non-cgo Go suites pass when run serially. Early overlapping
  runs encountered existing one-second subprocess-fixture timeouts; the affected
  tests pass in isolation and both complete serial suites pass.
- Focused tests cover label/system semantics, dry-run/idempotency, unrelated
  label/read preservation, accepted-but-unchanged responses, interrupted
  dispatch, durable receipts, stale confirmations, provider account binding,
  exact account names, aliases, and ambiguous identities.
- Real SQLite fixtures cover numeric read flags, full nested label paths,
  account boundaries, regenerated IDs, and independent Mail header readback.
  The fixture path is explicit; tests do not discover the user's Mail directory.
- Live Mail returns bare RFC Message-ID values and sender presentation/casing
  different from the index. Feature-local normalization and semantic sender
  guards cover those cases; global stable identity/archive policy is unchanged.
- Targeted race checks, vet, command surface, and whitespace checks pass.
- On home-server macOS 27.2, the focused adapter/CLI/surface tests and native
  build pass. These remote checks use no live Gmail credentials or mutations.

## Remaining delivery observations

- [ ] Verify the canonical PATH installation and binary provenance.
- [ ] Perform the authorized single-message production label removal and fresh
  provider/Mail observations; keep private identity evidence outside Git.
- [ ] Update PR #26 and inspect review/check state.

No conclusion about an actual Apple Mail Today view follows from these label
checks. The CLI's Today count is date-based and can include archived mail.
