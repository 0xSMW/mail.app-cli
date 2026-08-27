# mail-app-cli

A command line for macOS Mail.app. On a terminal it prints tables; in a pipe it prints a JSON envelope. Agents get the same commands, an exit-code table, and an embedded skill. All names, message IDs, addresses, mailboxes, and message content in the examples are fictional.

## Terminal UI

```bash
mail-app-cli tui                         # all inboxes
mail-app-cli tui -a "Example Account"    # one account's INBOX
mail-app-cli tui -a "Example Account" -m "Example Receipts" --message 100001
```

A three-pane client: mailboxes on the left, the message list in the middle, and the open message on the right (the reader takes the whole width on terminals narrower than 140 columns). Lists come from the Envelope Index, so switching mailboxes is quick; bodies and every action go through Mail.app one call at a time, with a spinner in the header while a call is queued.

| Key | Does |
|---|---|
| `j` `k` `g` `G` `pgup` `pgdn` | move |
| `enter` | open the message; `n` `p` step to the next or previous one while reading |
| `space` | select; actions then apply to the selection |
| `e` `#` `m` | archive, trash (asks first), move (mailbox picker with filtering) |
| `u` `!` | toggle read, toggle flag |
| `/` | search the account (or all accounts from All inboxes); `esc` leaves the results |
| `c` `r` `R` `f` | compose, reply, reply all, forward; `ctrl+s` sends, `ctrl+d` saves a draft, `esc` discards |
| `1`..`9` | jump to a mailbox in the sidebar |
| `tab` | cycle sidebar, list, reader |
| `ctrl+r` | refresh mailboxes and the list |
| `?` | hide or show the key bar |
| `q` | quit; `ctrl+c` twice also quits |

Actions apply on screen right away and the list refreshes from the index two seconds after Mail.app confirms. Because Mail.app renumbers a message when it moves, there is no undo; use `move` from the destination mailbox instead. `NO_COLOR` turns color off, and the TUI uses the terminal's own 16 ANSI colors, so it follows your theme.

## Features

- Inbox, unread, search, and per-mailbox listings from Mail's local Envelope Index (about 0.1s)
- Read, mark, flag, archive, delete, and move by message ID, one or many, with `--dry-run` on every mutation
- Bulk operations by query, sender, or domain with receipts, chunking, verification, and report files
- Drafts, sending with attachments and signatures, rules, smart mailboxes, threads, signatures, VIP mail
- Export messages and attachments; validate exported JSON
- `--json`, `--quiet`, `--jq`, `--ids-only`, `--count`, `--plain`; `NO_COLOR` respected
- Typed errors with a fixed exit-code table and a `hint`
- A default account and mailbox from a config file, env, or the only account Mail.app has
- `doctor`, `commands --json`, `--agent --help`, `skill install` for agents
- `tui`, an interactive three-pane client for triage, reading, and replying

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/0xSMW/mail.app-cli/master/install.sh | sh
```

Or with Go 1.24 or newer:

```bash
go install github.com/0xSMW/mail.app-cli/v2@latest
```

Grant Full Disk Access to the app that runs `mail-app-cli` (Terminal, iTerm, your editor, your agent host) so it can read Mail's Envelope Index. Without it, reads fall back to Mail.app automation and cross-mailbox search is refused. `mail-app-cli doctor` tells you which is the case.

## Quick start

```bash
mail-app-cli doctor                      # Mail.app reachable? index readable?
mail-app-cli accounts list
mail-app-cli config set account "Example Account"   # optional; with one account it is picked for you
mail-app-cli inbox                       # newest 25 across enabled accounts
mail-app-cli unread
mail-app-cli show 100001                 # body included; mailbox found for you
mail-app-cli seen 100001 100002
mail-app-cli flag 100001
mail-app-cli archive 100001 --dry-run
mail-app-cli archive 100001
mail-app-cli move 100001 --to "Example Receipts"
mail-app-cli search "sample invoice" --limit 20
mail-app-cli send -t recipient@example.test -s "Hello" --body "Hi" --dry-run
```

Every command has `--help`. `mail-app-cli help output`, `help exit-codes`, `help environment`, and `help agents` cover the contract.

## Output

On a terminal:

```
$ mail-app-cli inbox --limit 3
ID      DATE              FROM                  SUBJECT                          LOCATION
100003  Jan 15 13:50      Example Hosting       Sample maintenance notice        Example Account/INBOX
100002  Jan 15 12:40      Example Billing       Sample invoice                  Example Account/INBOX
100001  Jan 15 06:48  •   Example Reports       Your sample report is ready      Example Account/INBOX
```

`•` is unread, `⚑` is flagged.

Piped, or with `--json`:

```json
{
  "ok": true,
  "schemaVersion": 1,
  "data": [
    {"id": "100003", "subject": "Sample maintenance notice", "sender": "Example Hosting <updates@example.test>",
     "dateReceived": "2026-01-15T06:50:19Z", "read": true, "flagged": false, "mailbox": "INBOX", "account": "Example Account", ...}
  ],
  "summary": "3 messages in inbox",
  "meta": {"command": "inbox", "count": 3, "durationMs": 131, "view": "inbox"}
}
```

Errors go to stderr with a code and a hint:

```json
{"ok": false, "schemaVersion": 1, "error": "message not found: 0", "code": "not_found", "exitCode": 2, "hint": "..."}
```

| Flag | Effect |
|---|---|
| `--json` | envelope even on a terminal |
| `--plain` | table even in a pipe |
| `--quiet`, `-q` | bare `data`, no envelope |
| `--ids-only` | one ID per line from inbox/unread, search, account, message, draft, thread, smart-query, or recent-search lists; attachments do not support it |
| `--count` | just the number of items |
| `--jq EXPR` | on read commands and `export messages` to stdout, run a jq expression over the envelope (over `data` with `--quiet`); strings print raw. Commands that change state, including file exports, reject it. |
| `--no-color` | no ANSI; `NO_COLOR=1` does the same |

Set a default with `config set output json` or `MAIL_APP_CLI_OUTPUT=json`.

### jq examples

```bash
mail-app-cli inbox --jq '.data[] | select(.read == false) | .id'
mail-app-cli mailboxes list --jq '[.data[].unreadCount] | add'
mail-app-cli mailboxes list --jq '.data[] | select(.unreadCount > 0) | "\(.account)/\(.name): \(.unreadCount)"'
mail-app-cli search "important" --jq '.data[] | [.account, .mailbox, .subject, .sender] | @csv'
mail-app-cli attachments list 100002 --jq '.data[] | select(.fileSize > 1048576) | .name'
```

Or pipe to a real `jq`: `mail-app-cli inbox | jq '.data[].subject'`.

### Exit codes

| Exit | Code | When |
|---|---|---|
| 0 | | success |
| 1 | `usage` | bad flags or arguments, missing account, refused combination |
| 2 | `not_found` | message, account, mailbox, rule, draft, signature, attachment |
| 3 | `unavailable` | Mail.app missing, automation denied, index unreadable |
| 4 | `timeout` | a Mail.app call or its queue wait ran out of time |
| 5 | `partial` | cross-mailbox search incomplete without `--allow-partial` |
| 6 | `mutation_failed` | at least one requested change failed or did not verify |
| 7 | `internal` | anything else |

## Scope: account and mailbox

`--account`/`-a` and `--mailbox`/`-m` work on every command. Precedence is flag, then `MAIL_APP_CLI_ACCOUNT`/`MAIL_APP_CLI_MAILBOX`, then the config file, then defaults: the only enabled account, and `INBOX`. With several accounts and no default, account-scoped commands exit 1 and list the names.

The mailbox default applies to the commands that read one mailbox: `messages list`, `messages batch` selectors, `threads`, `export`, `import`, and `rules apply`. The unified inbox views (`inbox`, `unread`, `messages inbox`, and `messages unread`) respect the resolved scope: an account from `--account`, `MAIL_APP_CLI_ACCOUNT`, or config limits the view to that account's INBOX; a mailbox from `--mailbox`, `MAIL_APP_CLI_MAILBOX`, or config limits it to that mailbox. A mailbox scope needs an account, so the CLI uses the only enabled account when there is one or asks you to choose when there are several. With no configured account or mailbox, these views merge INBOX across enabled accounts. `search` and `sync` only narrow to a mailbox when `-m` is on the command line, and ID-driven verbs never use the default to guess where a message is.

```bash
mail-app-cli config set account "Example Account"
mail-app-cli config show
```

```
KEY      VALUE   SOURCE
account  Example Account  config
mailbox  INBOX   default
output   auto    default
```

Message IDs are numeric and global. `show`, `seen`, `unseen`, `flag`, `unflag`, `archive`, `delete`, `move`, and `attachments` look the mailbox up in the Envelope Index, so `-a` and `-m` are only needed to override. Pass `-m` when the index has not seen a message yet. On Gmail, `archive` acts from INBOX when the message carries that label and from All Mail otherwise (a no-op), so it never strips a user label; `move` and `delete` act from a user label when the message has one.

After archive, delete, or move, Mail.app gives the message a new ID in the destination mailbox. The receipt reports the ID you passed; find the message again with `search` or `recent search` before touching it a second time.

## Mutations

Every message mutation (`seen`, `unseen`, `flag`, `unflag`, `archive`, `delete`, `move`, the `messages *` spellings, `messages batch`, and `threads archive`) returns the same receipt and accepts `--dry-run` and `--verify`. Other mutations (`send`, `drafts *`, `rules *`) accept `--dry-run` and return their own shape.

```bash
$ mail-app-cli archive 100001 100002 --dry-run
Dry run: would have archived 2 messages
ID      STATUS   LOCATION      DETAIL
100001  dry-run  Example Account/INBOX    Your sample report is ready
100002  dry-run  Example Account/INBOX    Sample invoice
```

```json
{"action": "archive", "dryRun": false, "matched": 2, "attempted": 2, "succeeded": 2, "failed": 0,
 "items": [{"id": "100001", "account": "Example Account", "sourceMailbox": "INBOX", "targetMailbox": "All Mail", "status": "succeeded"}]}
```

`--verify` re-reads each message afterwards and records `verifyStatus`. A receipt with failures is still written, with `ok: false`, `code: "mutation_failed"`, and `exitCode: 6` in the same envelope, and the process exits 6. `ok` means "this command did what you asked", so check it, or check the exit code, before trusting a receipt.

Bulk selection by query, sender, or domain lives under `messages batch` and needs `--yes` for archive, delete, and move unless `--dry-run` is set:

```bash
mail-app-cli messages batch archive -a "Example Account" -m INBOX --sender-domain updates.example.test --limit 500 --dry-run
mail-app-cli messages batch archive -a "Example Account" -m INBOX --sender-domain updates.example.test --limit 500 --yes --chunk-size 50 --progress --verify --report-file cleanup.json
mail-app-cli search "sample alert" --ids-only | xargs mail-app-cli delete --dry-run
mail-app-cli messages batch mark --read=false 100003 100004
```

## Sending and drafts

```bash
mail-app-cli send -a "Example Account" -t recipient@example.test -s "Hello" --body "Message" --attach ~/sample.pdf --signature "Example Account"
mail-app-cli send -t recipient@example.test -s "Hello" --body-file body.md --dry-run
mail-app-cli drafts create -a "Example Account" --to recipient@example.test --subject "Review" --body-file body.md
mail-app-cli drafts list
mail-app-cli drafts update <draft-id> --subject "Updated"
mail-app-cli drafts send <draft-id>
mail-app-cli drafts delete <draft-id> --dry-run
```

## Search

```bash
mail-app-cli search "sample project update"
mail-app-cli search "sample invoice" -a "Example Account" --since 2026-01-01 --sender-domain billing.example.test
mail-app-cli search "sample invoice" -a "Example Account" -m "All Mail"
```

Every term must match the subject, sender, or Mail's indexed summary. Without `-m` the search covers every non-empty mailbox of each enabled account (or of the account named with `-a`). If a mailbox cannot be searched the command exits 5; `--allow-partial` returns what was found as `{messages, complete, searchedMailboxes, failedMailboxes}` in regular structured output. With `--ids-only` or `--count`, those list modifiers operate on `messages`. If the index is unreadable and live search fails, the recent-message journal is consulted; `--no-cache` disables that fallback.

## Other commands

```bash
mail-app-cli messages list -a "Example Account" -m INBOX --unread --since 2026-01-01 --limit 10
mail-app-cli messages sent|drafts|flagged|trash|junk
mail-app-cli attachments list 100002
mail-app-cli attachments save 100002 "sample-invoice.pdf" -o ~/Downloads/sample-invoice.pdf
mail-app-cli export messages -a "Example Account" -m INBOX --output inbox.json
mail-app-cli export attachments -a "Example Account" -m INBOX --output ./attachments
mail-app-cli import messages -a "Example Account" -m Archive --file inbox.json --dry-run
mail-app-cli threads list -a "Example Account" -m INBOX
mail-app-cli rules list
mail-app-cli rules create "Example Receipts" -a "Example Account" --from-domain billing.example.test --move-to "Example Receipts" --dry-run
mail-app-cli smart list
mail-app-cli signatures show "Example Account"
mail-app-cli messages vip
mail-app-cli recent search "sample project update"
mail-app-cli sync --wait
```

`messages show|mark|flag|archive|delete|move` are the 1.x spellings and still work; they now return receipts and accept `--dry-run`.

## For agents

```bash
mail-app-cli skill            # print the embedded SKILL.md
mail-app-cli skill install    # ~/.claude/skills/mail-app-cli/SKILL.md
mail-app-cli commands --json  # every command, flag, and agent note
mail-app-cli show --agent --help
```

`help agents` is the short version: always `--json`, check `ok`, read the exit code, dry-run before acting on more than a couple of messages, and never `send` without showing the user a `--dry-run` first.

## Migrating from 1.x

| 1.x | 2.0 |
|---|---|
| `jq '.[].Subject'` | `jq '.data[].subject'` or `--jq '.data[].subject'` |
| bare array output | `--quiet` |
| `messages archive ID -a A -m M` | `archive ID` |
| `messages mark ID --read=false` | `unseen ID` |
| "Message archived" text | receipt JSON, or one line on a terminal |
| exit 1 for every failure | see the exit-code table |
| `sync --json` | `sync` (global `--json`) |
| `... \| jq length` | `... \| jq '.data \| length'` or `--count` |
| stderr was a bare string | stderr is a JSON error envelope when piped, text on a terminal |
| `--version` printed `mail-app-cli version 1.3.0` | `mail-app-cli 2.0.0` |
| `doctor` always exited 0 | exits 3 when not healthy |
| `search --no-cache` was accepted and ignored | disables the recent-journal fallback |

Keys are camelCase everywhere. Lists are never `null`. Warnings that 1.x printed as free text on stderr are `notices` in the envelope.

## Environment

| Variable | Purpose |
|---|---|
| `MAIL_APP_CLI_ACCOUNT`, `MAIL_APP_CLI_MAILBOX` | default scope |
| `MAIL_APP_CLI_OUTPUT` | `auto`, `json`, or `plain` |
| `MAIL_APP_CLI_CONFIG` | config file path (default `~/.config/mail-app-cli/config.json`) |
| `MAIL_APP_CLI_SKILL_DIR` | where `skill install` writes |
| `MAIL_APP_CLI_SEARCH_TIMEOUT` | seconds for automation search (8) |
| `MAIL_APP_CLI_CONTENT_TIMEOUT` | seconds for `--with-content` (45) |
| `MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX` | force Mail.app automation for reads |
| `NO_COLOR` | disable color |

Caches live in `~/.cache/mail-app-cli/` (accounts and mailboxes 24h, message lists 5 minutes, recent-message journal).

## How it works

Reads come from Mail's Envelope Index, a SQLite file, when it is readable, and from Mail.app through JavaScript for Automation otherwise. Writes and message bodies always go through Mail.app automation, serialized across goroutines and processes because Mail.app's scripting bridge is not safe under concurrent requests. Each automation call is bounded by a timeout and runs in its own process group.

## Development

```bash
go build -o mail-app-cli
go vet ./... && go test ./...
UPDATE_SURFACE=1 go test ./cmd -run TestSurfaceSnapshot   # after adding or removing commands or flags
```

`docs/cli-ergonomics-plan.md` explains the 2.0 design; `docs/tui-plan.md` covers the terminal UI that builds on it; `plans/tars-real-world-reliability.md` tracks mutation integrity and stable identity work.

## Requirements

- macOS 15 or newer with Mail.app configured
- Go 1.24 or newer to build

---

This project is a hard fork of [intelligrit/mail-app-cli](https://github.com/intelligrit/mail-app-cli).
