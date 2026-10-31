---
name: mail-app-cli
description: Read, search, triage, and send mail through macOS Mail.app with mail-app-cli. Use when asked about someone's inbox, a specific email, unread mail, archiving or flagging messages, drafts, or sending from Mail.app.
---

# mail-app-cli

A Go CLI over macOS Mail.app. Reads are fast (Mail's local Envelope Index); writes go through serial Mail.app automation. Selected bodies come from local message files; identity verification reads metadata without rendering bodies.

## Invariants

- Add `--json` to every call and check `ok` (or the exit code) before trusting `data`. `ok: false` with data present means a receipt with failures or an unhealthy `doctor`; pure failures are on stderr as `{"ok": false, "code", "error", "hint", "exitCode"}`. Warnings are `notices` in the envelope.
- Exit codes: 1 usage, 2 not found, 3 Mail.app unavailable (run `doctor`), 4 timeout, 5 incomplete search/scan/body read, 6 mutation failed, 7 internal.
- IDs are numeric strings from `inbox`, `search`, `messages list`, and `show`. An ID alone is enough for every verb; the CLI finds the mailbox. Pass `--account`/`--mailbox` only when you know the CLI is wrong.
- Every message mutation (`seen`, `unseen`, `flag`, `unflag`, `archive`, `delete`, `move`, `messages batch`) takes `--dry-run` and `--verify` and returns a receipt: `{action, dryRun, matched, succeeded, failed, skipped, items: [{id, account, sourceMailbox, targetMailbox, status, error}]}`. `send`, `drafts`, and `rules` take `--dry-run` and return their own shapes. Preview before acting on more than a couple of messages.
- `send` is irreversible. Use `send --dry-run` to show the user the message, or `drafts create` when they should review in Mail.app.
- `search` exits 5 when any mailbox could not be searched. Only add `--allow-partial` when the user accepts incomplete results.
- Field names are camelCase: `id`, `subject`, `sender`, `dateReceived`, `read`, `flagged`, `mailbox`, `account`.

## Quick reference

| Task | Command |
|---|---|
| Health and permissions | `mail-app-cli doctor --json` |
| Accounts | `mail-app-cli accounts list --json` |
| Mailboxes and unread counts | `mail-app-cli mailboxes list --json` |
| Inbox across accounts | `mail-app-cli inbox --json --limit 25` |
| Unread only | `mail-app-cli unread --json` |
| One mailbox with filters | `mail-app-cli messages list -a "Example Account" -m INBOX --unread --since 2026-01-01 --json` |
| Explicit live mailbox scan | `mail-app-cli messages scan INBOX 'All Mail' Spam -a "Example Account" --since 2026-01-01 --limit 200 --json` |
| Search selected mailboxes | `mail-app-cli messages scan INBOX 'All Mail' Trash -a "Example Account" --query 'sample invoice' --json` |
| Resume historical scan | `mail-app-cli messages scan INBOX Archive -a "Example Account" --checkpoint /absolute/private/scan.json --limit 200 --json` |
| Recheck a relocation receipt | `mail-app-cli messages reconcile /absolute/private/receipt.json --settle 10s --verify-budget 30s --json` |
| Inspect body alternatives | `mail-app-cli messages read 12345 --body-alternatives --json` |
| Search | `mail-app-cli search "sample invoice" --json --limit 20` |
| Read a message | `mail-app-cli show 12345 --json` |
| Inspect local unsubscribe opportunities | `mail-app-cli unsubscribe show 12345 --json` |
| Discover and group unsubscribe opportunities in folders | `mail-app-cli unsubscribe scan INBOX Archive -a "Example Account" --json` |
| Resume unsubscribe discovery | `mail-app-cli unsubscribe scan INBOX Archive -a "Example Account" --checkpoint /private/discovery.json --json` |
| Discover all link destinations | `mail-app-cli links 12345 --json` |
| Discover unsubscribe and preferences links | `mail-app-cli links 12345 --unsubscribe --json` |
| Print all links alongside the body | `mail-app-cli show 12345 --links --plain` |
| Read selected bodies | `mail-app-cli messages read 12345 67890 --timeout 10s --budget 45s --json` |
| Mark read before relocating | `mail-app-cli messages batch archive 12345 67890 --mark-read --verify --dry-run --json`, then without `--dry-run` |
| Headers only, no body fetch | `mail-app-cli show 12345 --metadata-only --json` |
| Mark read / unread | `mail-app-cli seen 12345 67890 --json`, `unseen` |
| Flag / unflag | `mail-app-cli flag 12345 --json`, `unflag` |
| Archive | `mail-app-cli archive 12345 --json` |
| Trash | `mail-app-cli delete 12345 --dry-run --json`, then without `--dry-run` |
| Move | `mail-app-cli move 12345 --to "Receipts" --json` |
| Gmail label state | `mail-app-cli labels show 12345 -a "Example Account" --json` |
| Remove Important or a user label without moving | `mail-app-cli labels remove 12345 12346 -a "Example Account" --label IMPORTANT --dry-run --json`, then without `--dry-run` |
| Gmail credential check | `mail-app-cli labels auth status -a "Example Account" --json` |
| Bulk by selector | `mail-app-cli messages batch archive -a "Example Account" -m INBOX --sender-domain updates.example.test --dry-run --json`, then `--yes` |
| Attachments | `mail-app-cli attachments list 12345 --json`, `attachments save 12345 "file.pdf" -o ~/Downloads/file.pdf` |
| Send | `mail-app-cli send -t recipient@example.test -s "Subject" --body "text" --dry-run --json` |
| Draft for review | `mail-app-cli drafts create -a "Example Account" --to recipient@example.test --subject "S" --body-file body.md --json` |
| Attach files to a draft | `mail-app-cli drafts update <draft-id> --attach report.pdf --attach chart.png --json` |
| Recently handled | `mail-app-cli recent search "sample invoice" --json` |
| Settings | `mail-app-cli config show --json`, `config set account "Example Account"` |
| Every command and flag | `mail-app-cli commands --json` |

`mail-app-cli tui` is the interactive client for a person at a terminal; do not run it from a script.

Shortcuts on read-only lists: `--count`, `--jq '.data[] | select(.read == false) | .id'`, and `--quiet` for bare data. State-changing commands reject `--jq` before doing any work. Use `--ids-only` on ID-bearing inbox, search, account, message, draft, thread, smart-query, and recent-search lists.

## Workflow

For repeated triage, scan metadata first and deduplicate account/local-ID pairs
before reading selected bodies. Avoid broad `list --with-content` calls. `scan`
is always live; inspect `complete` and each coverage entry before concluding a
mailbox is clear. Truncation exits 5 just like failed coverage. Each coverage
entry gives `exhausted` and `remaining`; while `nextCursor` is present, repeat the
same arguments with `--cursor` until it is absent. Keep required unfiltered
completion checks. `--since` filters receipt time, so it misses older messages
whose read state or labels changed. A scan preserves observed mailbox memberships
and is not an atomic snapshot or change cursor.

Selected `read` returns successful bodies alongside per-message errors; missing
or timed-out content is unknown, not empty. Bodies come from Mail.app's message
files (`contentSource: "disk"`), so reads do not wait on Mail.app to render;
pass `--body-source mail` only to compare. Each item has `diagnostics` with
per-phase timings; after a timeout, `pending` names the operation Mail.app did
not answer. Retry only the unresolved IDs after checking the failure. Keep Mail operations serial. Group compatible cleanup into
previewed batches with `--mark-read --verify`, rather than separate mark and move
passes. Leave the default chunk size unless needed; compatible batches reuse a
serial bridge with durable receipts. A verified archive or move reports
`observed.sourcePresent`, `destinationPresent`, `destinationRead`, and the current
local IDs in `sourceIds` and `destinationIds`. Act on those IDs, not the ID you
passed in, when a copy reappears in the source. Gmail
All Mail presence alone does not prove INBOX absence.

1. If a command exits 3, run `doctor --json`. `healthy` covers the live Mail.app bridge only. `envelopeIndexAvailable: false` means Full Disk Access is missing for the terminal or agent host; reads still work but are slow, and cross-mailbox `search` is refused.
2. When the user has several accounts and none is configured, account-scoped commands exit 1 listing the names. Ask which, or use `-a`.
3. To act on search results: `search ... --ids-only | xargs mail-app-cli archive --json`.
4. After a mutation from outside the CLI, add `--no-cache` to `messages list`; its results are cached for five minutes.
5. Gmail: archive means "move to All Mail". A message already in All Mail is reported as `status: skipped` with `error: "already in All Mail"`, counted in `skipped`, and the command exits 0; nothing was relocated. `--mark-read` alone does not change skipped items. Add `--mark-read-on-skip` with `--mark-read` to explicitly mark and verify already-archived records.
6. Moving a message (archive, delete, move) makes Mail.app assign it a new ID in the destination. The receipt reports the old ID; to touch the message again, find it with `search` or `recent search`. Don't chain a second mutation on the same ID after a move.
7. Gmail archive removes Inbox membership while preserving Important and user labels. A move may remove its source user-label membership. To remove only Important or a specific user label, use `labels remove`. It needs its own Gmail credentials, separate from Mail.app's sign-in; without them it exits 3 with a hint that names the command to connect them. Several IDs must be in one account, run in order, and stop at the first failure.

## Migration from 1.x

`.[]` is now `.data[]`; PascalCase keys are camelCase; `--quiet` gives the old bare shape. `messages archive|delete|move|mark|flag` still work and now return receipts.

## Durable traversal and final verification

`scan --checkpoint PATH` persists private cumulative messages, memberships and
coverage before output. Repeat the same scopes and filters to continue; the page
limit may change. Completed checkpoints require `--restart-checkpoint` for a
fresh traversal. Checkpoint output includes traversal and current-page observation
times. Earlier rows are earlier observations, not current state or a changed-mail
feed. Keep an advancing receipt-time review window separate from held identities
and historical traversal. Reconcile older held identities explicitly.

For verified archive/move, use `--settle 10s --verify-budget 30s` when provider
synchronization needs a longer final observation. Receipts include stable
identities, identity capture timings and final observed scopes/read state.
`messages reconcile RECEIPT` rechecks a JSON receipt, envelope or JSONL journal
without replaying mutations or synchronizing accounts. It includes previously
confirmed identities. A later observation cannot guarantee no future recurrence;
never automatically change archive intent to delete. Requested destination read
state must agree before relocation is confirmed. Gmail All Mail retention is
allowed only for a verified Gmail restore to INBOX; Spam removal and archive
INBOX removal remain required.

Successful body extraction (`complete:true`) is not a guarantee of full visual
or semantic content. Inspect `bodyDiagnostics`; `--body-alternatives` exposes
other extracted renderings without remote fetching. Counts and alternate-text
differences establish extraction facts, not the meaning of omitted content.
Mail Today's aggregate counts cannot prove handled-identity cleanup and are not
a completion gate; use fresh direct source/destination checks.

## Link discovery

For unsubscribe-specific tasks, use `unsubscribe show ID` or `unsubscribe scan
MAILBOX...`. Both inspect local headers and every supported body alternative,
without per-message Mail header/body fallback. Groups use List-Id where available;
fallback sender/header-host grouping is provisional. Exact destinations retain
all provenance, older-only controls, and message/folder evidence. RFC-identical
copies count once within a group. Freshness and one-click eligibility remain
unverified.

`unsubscribe scan` enumerates every page until exhausted or the invocation budget
is reached. `--page-size` is not sampling; `--since` explicitly bounds receipt
dates. Use `--all-accounts` only when all enabled accounts are requested. Every
run has a private checkpoint, generated automatically unless `--checkpoint` is
given. Repeat the returned checkpoint with the original scopes/filter to resume;
`--max-pages 1` bounds one invocation. Pending work is persisted before advancing.
A completed checkpoint replays stored results; use `--restart-checkpoint` for
fresh observations. `--retry-incomplete` rechecks earlier partial/missing sources.

Always inspect `complete`, `traversalComplete`, `pending`, `coverage`,
`readability`, and `exceptions`. Exit 5 retains groups and means unresolved
coverage or extraction; a known control does not establish complete discovery.
Never fall back automatically to broad `list --with-content` or repeated Mail
rendering for missing sources. An exhausted traversal is still a sequence of
observations, not an atomic snapshot. Discovery does not authorize visiting links
or performing unsubscribe actions.

Use `links ID --json` to retrieve destinations without opening URLs, fetching
remote resources, sending mail, or unsubscribing. It reads local MIME, including
partial files, and uses a header-only fallback when the file is unavailable.
It never requests Mail body rendering. Exit 5 retains recovered links with
`linkStatus.complete: false`; an incomplete empty inventory is unknown.

Full `show` and `messages read` results also carry `links`, `linkStatus`, and
`unsubscribe`. Link fields are `url`, `text`, `source` (`html`, `plain`, or
`header`), `mimePart`, `selected`, `kind` (`link`, `unsubscribe`, or
`preferences`), and `openable`. `selected: false` denotes a MIME alternative.
`--unsubscribe` filters both unsubscribe and preferences candidates. Preserve
exact destinations, including personalized query strings; do not infer an
unsubscribe URL from a homepage. Check provenance and available candidates.

Unsubscribe metadata includes raw `listUnsubscribe` and `listUnsubscribePost`
header values, `headerStatus`, and `oneClickAdvertised`. An advertisement is
not verified one-click eligibility. Retrieving destinations does not authorize
opening one, sending a mailto request, or performing an unsubscribe POST.
