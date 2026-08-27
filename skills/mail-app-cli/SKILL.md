---
name: mail-app-cli
description: Read, search, triage, and send mail through macOS Mail.app with mail-app-cli. Use when asked about someone's inbox, a specific email, unread mail, archiving or flagging messages, drafts, or sending from Mail.app.
---

# mail-app-cli

A Go CLI over macOS Mail.app. Reads are fast (Mail's local Envelope Index); writes and message bodies go through Mail.app automation, one call at a time, 0.4s to 2s each.

## Invariants

- Add `--json` to every call and check `ok` (or the exit code) before trusting `data`. `ok: false` with data present means a receipt with failures or an unhealthy `doctor`; pure failures are on stderr as `{"ok": false, "code", "error", "hint", "exitCode"}`. Warnings are `notices` in the envelope.
- Exit codes: 1 usage, 2 not found, 3 Mail.app unavailable (run `doctor`), 4 timeout, 5 partial search, 6 mutation failed, 7 internal.
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
| Search | `mail-app-cli search "sample invoice" --json --limit 20` |
| Read a message | `mail-app-cli show 12345 --json` |
| Headers only, no body fetch | `mail-app-cli show 12345 --metadata-only --json` |
| Mark read / unread | `mail-app-cli seen 12345 67890 --json`, `unseen` |
| Flag / unflag | `mail-app-cli flag 12345 --json`, `unflag` |
| Archive | `mail-app-cli archive 12345 --json` |
| Trash | `mail-app-cli delete 12345 --dry-run --json`, then without `--dry-run` |
| Move | `mail-app-cli move 12345 --to "Receipts" --json` |
| Bulk by selector | `mail-app-cli messages batch archive -a "Example Account" -m INBOX --sender-domain updates.example.test --dry-run --json`, then `--yes` |
| Attachments | `mail-app-cli attachments list 12345 --json`, `attachments save 12345 "file.pdf" -o ~/Downloads/file.pdf` |
| Send | `mail-app-cli send -t recipient@example.test -s "Subject" --body "text" --dry-run --json` |
| Draft for review | `mail-app-cli drafts create -a "Example Account" --to recipient@example.test --subject "S" --body-file body.md --json` |
| Recently handled | `mail-app-cli recent search "sample invoice" --json` |
| Settings | `mail-app-cli config show --json`, `config set account "Example Account"` |
| Every command and flag | `mail-app-cli commands --json` |

Shortcuts on any list: `--count`, `--jq '.data[] | select(.read == false) | .id'`, and `--quiet` for bare data. Use `--ids-only` on ID-bearing inbox, search, account, message, draft, thread, smart-query, and recent-search lists.

## Workflow

1. If a command exits 3, run `doctor --json`. `healthy` covers the live Mail.app bridge only. `envelopeIndexAvailable: false` means Full Disk Access is missing for the terminal or agent host; reads still work but are slow, and cross-mailbox `search` is refused.
2. When the user has several accounts and none is configured, account-scoped commands exit 1 listing the names. Ask which, or use `-a`.
3. To act on search results: `search ... --ids-only | xargs mail-app-cli archive --json`.
4. After a mutation from outside the CLI, add `--no-cache` to `messages list`; its results are cached for five minutes.
5. Gmail: archive means "move to All Mail". A message already in All Mail archives as a no-op with `status: succeeded`.
6. Moving a message (archive, delete, move) makes Mail.app assign it a new ID in the destination. The receipt reports the old ID; to touch the message again, find it with `search` or `recent search`. Don't chain a second mutation on the same ID after a move.

## Migration from 1.x

`.[]` is now `.data[]`; PascalCase keys are camelCase; `--quiet` gives the old bare shape. `messages archive|delete|move|mark|flag` still work and now return receipts.
