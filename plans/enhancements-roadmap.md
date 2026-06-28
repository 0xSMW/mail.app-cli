# Mail.app CLI Enhancements Roadmap

## Exact Analysis

The best overall approach is to make this CLI a reliable automation layer over Mail.app: prioritize high-leverage primitives that let scripts inspect, select, preview, and mutate mail safely. The current tool already covers accounts, mailboxes, messages, attachments, search, send, unified views, and account sync; the roadmap should build around composable JSON workflows, dry-runs, idempotent operations, and bulk-safe execution.

- Build first: **batch operations**, **export/import**, and **draft management**. These unlock real CLI workflows: migrations, triage scripts, backups, scripted replies, and repeatable inbox cleanup.
- Build next: **rules**, **smart mailboxes**, and **threading**. These make the CLI useful for higher-level organization and review.
- Build later: **signatures**, **VIP contacts**, and deeper **IMAP folder sync**. Useful, but lower leverage unless the CLI becomes a daily driver or automation backend.
- Design principle: every mutating command should support `--dry-run`, `--json`, clear selectors, and safe batching.

| Enhancement | CLI Capability | How It Would Work | Why It Matters |
|---|---|---|---|
| Rules management | List/create/edit/delete Mail.app rules | `rules list`, `rules create --from ... --move-to ...`, `rules apply --dry-run` | Turns inbox automation into versionable shell scripts. |
| Smart mailbox operations | Create/list/query smart mailboxes | `smart list`, `smart create --unread --from-domain example.com` | Lets users save reusable search views from automation. |
| Signatures management | List/set/apply signatures | `signatures list`, `send --signature Work` | Enables scripted sending without hand-managed body templates. |
| VIP contacts | List/add/remove VIP senders | `vip list`, `vip add person@example.com` | Makes notification and priority workflows scriptable. |
| Export/import functionality | Backup or move mail data | `export --mailbox INBOX --format mbox/json`, `import --file ...` | Critical for migration, archival, audit, and recovery. |
| Batch operations | Apply actions to many messages safely | `messages batch archive --query ... --dry-run`, or stdin IDs | Biggest immediate win: fast triage, cleanup, and repeatable workflows. |
| IMAP folder synchronization | Sync selected folders and report status | `sync --account Gmail --mailbox INBOX --wait --status` | Useful for scripts that need fresh state before acting. |
| Message threading support | Group/list/show conversations | `threads list --mailbox INBOX`, `threads show <id>` | Makes CLI review closer to how people actually process email. |
| Draft management | Create/list/edit/send/delete drafts | `drafts create`, `drafts edit <id>`, `drafts send <id>` | Enables approval workflows, generated replies, and delayed/manual sending. |

## Implementation Guidance

Use this plan to guide future implementation work. The goal is to expand the CLI as a dependable local automation surface for Mail.app while preserving the current project style: Cobra commands in `cmd/`, Mail.app/JXA/AppleScript access in `pkg/mail/client.go`, JSON-first output, and shell-friendly behavior.

## Current Baseline

The CLI already exposes:

- Accounts: `accounts list`, `accounts show`.
- Mailboxes: `mailboxes list`.
- Messages: list, show, mark, flag, delete, archive, move, unified inbox/unread/sent/drafts/flagged/trash/junk views.
- Search: subject/sender search across indexed messages.
- Attachments: list and save.
- Sending: direct send with recipients and attachments.
- Sync: sync all accounts or one account.
- Bulk client helpers already exist in `pkg/mail/client.go`: `BulkMarkMessages`, `BulkFlagMessages`, `BulkDeleteMessages`, `BulkArchiveMessages`, and `BulkMoveMessages`.

Prefer building on those existing primitives before adding new abstractions.

## Cross-Cutting CLI Contracts

All new commands should follow these contracts unless there is a strong reason not to:

- JSON output by default for data-returning commands.
- Human-readable stderr/status messages only for successful mutations when no JSON result is requested.
- `--dry-run` for every command that mutates mail, rules, signatures, smart mailboxes, VIPs, imports, or drafts.
- `--account`, `--mailbox`, `--limit`, `--offset`, `--since`, `--unread`, and `--flagged` should behave consistently with existing message commands when relevant.
- Support stdin IDs for batch-style commands: one message ID per line.
- Prefer explicit selectors over implicit global actions.
- Return structured per-item results for batch mutations, including successes, failures, skipped items, and source mailbox/account.
- Invalidate affected caches after mutations.
- Keep provider-specific behavior isolated in helper functions, especially Gmail archive/all-mail aliases.
- Avoid background daemons or external APIs; this project should remain a Mail.app-backed local CLI.

## Recommended Build Order

1. Batch operations.
2. Export functionality.
3. Draft management.
4. Import functionality.
5. Rules management.
6. Smart mailbox operations.
7. Message threading support.
8. IMAP folder sync status/waiting.
9. Signatures management.
10. VIP contacts.

This order maximizes immediate automation value while keeping risk controlled. Batch operations mostly wrap existing message helpers. Export and drafts are natural extensions of existing message and send flows. Rules, smart mailboxes, signatures, and VIPs depend more heavily on Mail.app scriptability details and should come after the safer primitives are established.

## Enhancement Details

### Batch Operations

Proposed commands:

```bash
mail-app-cli messages batch archive --account Gmail --mailbox INBOX --query "receipt" --dry-run
mail-app-cli messages batch mark --read=false --account Gmail --mailbox INBOX --unread=false
mail-app-cli messages batch move "Receipts" --account Gmail --mailbox INBOX < ids.txt
mail-app-cli messages batch delete --account Gmail --mailbox INBOX --stdin --dry-run
```

Implementation notes:

- Add a `messages batch` command group in `cmd/messages.go` or a new `cmd/messages_batch.go`.
- Reuse existing list/search code to resolve target IDs.
- Reuse existing `Bulk*` methods in `pkg/mail/client.go`.
- Return a result object with `matched`, `attempted`, `succeeded`, `failed`, and `items`.
- Require `--yes` for destructive bulk delete/archive/move unless `--dry-run` is present.
- Cap default batch size and add `--limit` to avoid accidental huge actions.

Acceptance checks:

- Dry-run performs no mutation and prints selected message IDs.
- Real mutation invalidates source and destination mailbox caches.
- Partial failures are visible in JSON and produce a non-zero exit when any item fails.

### Export Functionality

Proposed commands:

```bash
mail-app-cli export messages --account Gmail --mailbox INBOX --format json --output inbox.json
mail-app-cli export messages --account Gmail --mailbox INBOX --format eml --output ./mail-export/
mail-app-cli export attachments --account Gmail --mailbox INBOX --output ./attachments/
```

Implementation notes:

- Start with JSON export because current message structs already support it.
- Add EML/mbox only after verifying Mail.app can expose raw source reliably.
- For attachment export, build on `GetAttachmentsJSON` and `SaveAttachment`.
- Include metadata: account, mailbox, export timestamp, CLI version, filters used.
- Use stable filenames for file exports: date, sanitized sender, subject, message ID.

Acceptance checks:

- Exported JSON can be parsed by `jq`.
- Export respects filters and limits.
- Existing messages remain unchanged.
- Attachment name collisions are handled deterministically.

### Draft Management

Proposed commands:

```bash
mail-app-cli drafts list --account Gmail
mail-app-cli drafts create --account Gmail --to user@example.com --subject "Review" --body-file body.md
mail-app-cli drafts show <draft-id> --account Gmail
mail-app-cli drafts update <draft-id> --account Gmail --subject "Updated"
mail-app-cli drafts send <draft-id> --account Gmail
mail-app-cli drafts delete <draft-id> --account Gmail --dry-run
```

Implementation notes:

- Existing `messages drafts` only lists unified draft messages. Add a first-class `drafts` command for mutations.
- Reuse send command recipient parsing.
- Add support for `--body-file` to both `send` and draft creation if absent.
- Sending a draft should return the sent message metadata if available.

Acceptance checks:

- Creating a draft does not send it.
- Updating a draft preserves unspecified fields.
- Sending a draft removes or changes the draft state in Mail.app.

### Import Functionality

Proposed commands:

```bash
mail-app-cli import messages --account Gmail --mailbox Archive --format json --file inbox.json --dry-run
mail-app-cli import messages --account Gmail --mailbox Archive --format eml --path ./eml/
```

Implementation notes:

- Treat import as higher risk than export.
- Start with rehydrating JSON into drafts or target mailbox only if Mail.app supports reliable import through scriptable APIs.
- If raw message import is unreliable, document export-only support and defer full import.
- Validate input before mutation and report every would-create item in dry-run.

Acceptance checks:

- Dry-run validates all source files.
- Import refuses ambiguous target accounts/mailboxes.
- Failed imports do not hide partial success.

### Rules Management

Proposed commands:

```bash
mail-app-cli rules list
mail-app-cli rules show <rule-name>
mail-app-cli rules create "Receipts" --from-domain stripe.com --move-to Receipts --enabled
mail-app-cli rules enable <rule-name>
mail-app-cli rules disable <rule-name>
mail-app-cli rules delete <rule-name> --dry-run
mail-app-cli rules apply <rule-name> --account Gmail --mailbox INBOX --dry-run
```

Implementation notes:

- Mail.app rules may expose conditions and actions through AppleScript.
- Start with listing and enable/disable before create/edit/delete.
- Model rule conditions/actions as structured JSON instead of opaque strings.
- Applying a rule should use the same selection engine as batch operations where possible.

Acceptance checks:

- Listing shows enabled state, conditions, and actions.
- Dry-run apply returns matched messages.
- Create validates target mailbox existence.

### Smart Mailbox Operations

Proposed commands:

```bash
mail-app-cli smart list
mail-app-cli smart show "Unread Receipts"
mail-app-cli smart create "Unread Receipts" --unread --from-domain stripe.com
mail-app-cli smart delete "Unread Receipts" --dry-run
```

Implementation notes:

- Confirm Mail.app scriptability for smart mailbox creation before implementing mutation.
- If creation is brittle, start with listing and querying existing smart mailboxes.
- Reuse search/filter semantics from messages list and search.

Acceptance checks:

- Listing smart mailboxes does not require account/mailbox flags.
- Querying a smart mailbox returns normal message JSON.
- Mutating commands are guarded by dry-run and exact names.

### Message Threading Support

Proposed commands:

```bash
mail-app-cli threads list --account Gmail --mailbox INBOX
mail-app-cli threads show <thread-id> --account Gmail --mailbox INBOX
mail-app-cli threads archive <thread-id> --dry-run
```

Implementation notes:

- Prefer Mail.app conversation/thread identifiers if exposed.
- If unavailable, derive a local thread key from normalized subject, sender/recipient set, and message references when available.
- Include `messages` count, unread count, latest date, participants, subject, and message IDs.
- Thread-level mutations should call batch operations internally.

Acceptance checks:

- Thread grouping is deterministic.
- Thread mutations report every message affected.
- Single-message threads still work.

### IMAP Folder Synchronization

Proposed commands:

```bash
mail-app-cli sync --account Gmail --mailbox INBOX --wait --timeout 60
mail-app-cli sync status --account Gmail
```

Implementation notes:

- Existing sync supports all accounts or one account.
- Add mailbox-scoped intent if Mail.app exposes it; otherwise sync the account and make that clear in JSON output.
- `--wait` should poll mailbox counts or last message date until stable or timeout.
- Add `--json` output for sync results.

Acceptance checks:

- Sync returns account, requested mailbox, actual sync scope, start/end time, and status.
- Timeout exits non-zero with a structured error.
- Existing `sync` behavior remains compatible.

### Signatures Management

Proposed commands:

```bash
mail-app-cli signatures list
mail-app-cli signatures show "Work"
mail-app-cli signatures apply "Work" --account Gmail
mail-app-cli send --signature "Work" --account Gmail --to user@example.com --subject "Hello"
```

Implementation notes:

- Start by listing signatures and using one at send time.
- Keep signature storage/selection compatible with Mail.app rather than inventing a separate template system.
- If Mail.app does not expose reliable signature assignment for composed messages, fall back to appending signature content to body with explicit docs.

Acceptance checks:

- Signature list includes name and account binding if available.
- `send --signature` sends body plus selected signature once.
- Missing signature fails before sending.

### VIP Contacts

Proposed commands:

```bash
mail-app-cli vip list
mail-app-cli vip add person@example.com
mail-app-cli vip remove person@example.com --dry-run
mail-app-cli messages vip --limit 25
```

Implementation notes:

- Verify whether Mail.app exposes VIPs directly.
- If VIP mutation is unavailable through Mail.app, support read-only VIP message views first.
- Avoid managing Contacts.app unless explicitly added as a dependency surface.

Acceptance checks:

- VIP list is deterministic.
- VIP message view returns normal message JSON.
- Mutations fail clearly if unsupported by the local Mail.app scriptability layer.

## Suggested File Layout

- `cmd/messages_batch.go` for batch message commands.
- `cmd/export.go` and `cmd/import.go` for data movement.
- `cmd/drafts.go` for first-class draft operations.
- `cmd/rules.go`, `cmd/smart.go`, `cmd/signatures.go`, `cmd/vip.go`, and `cmd/threads.go` for new command groups.
- `pkg/mail/client.go` can keep Mail.app integration initially, but split into focused files once it grows: `rules.go`, `drafts.go`, `export.go`, `threads.go`, etc.
- Add tests around command argument validation and pure helper functions first; Mail.app integration tests can remain manual or opt-in.

## Model Execution Notes

When implementing from this plan:

- Inspect current command patterns before adding new flags.
- Keep changes narrow and commit-ready by enhancement area.
- Prefer implementing one command group at a time.
- Start with read-only/list commands for scriptability-uncertain Mail.app features.
- Do not silently suppress unsupported Mail.app operations. Return explicit errors with the unsupported capability and the attempted command.
- Add README examples when a command is actually implemented.
- Run `go test ./...` after each enhancement group.
- For any command that changes mail state, manually test against a small disposable mailbox or draft before marking the task complete.
