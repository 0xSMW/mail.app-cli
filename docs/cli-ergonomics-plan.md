# CLI ergonomics plan

Target: make `mail-app-cli` pleasant for a person at a terminal and predictable for an agent driving it from a script, without giving up anything the 1.x command set already does. The reference point is `hey-cli` (`~/Code/ecosystem/hey-cli`), which solved the same problems for HEY. This document is the plan for the 2.0.0 release; a companion document, `docs/tui-plan.md`, covers the terminal UI that builds on it.

## Why a major version

Two things have to break, so they break together:

1. The JSON casing is split. Core types in `pkg/mail/models.go` have no json tags and serialize as `ID`, `Subject`, `UnreadCount`. Newer types (batch results, doctor, rules, search results) are camelCase. The README's own `jq '.[].subject'` examples return `null` against the real output. Fixing this changes every consumer.
2. Success output moves into an envelope so agents can read `ok`, `summary`, and `meta` without guessing. `.[]` becomes `.data[]`.

Anything that already parses 1.x output has to change. `--quiet` prints the bare `data` value, which is the migration path: add `--quiet`, rename PascalCase keys, done.

## What hey-cli does that we copy

| hey-cli mechanism | Where it lives there | What we build |
|---|---|---|
| One `output.Writer` chosen in `PersistentPreRunE`, styled on a TTY, JSON when piped | `internal/output/writer.go`, `internal/cmd/root.go` | `internal/output` package, wired in `cmd/root.go` |
| JSON envelope `{ok, data, summary, notice, meta}`; errors `{ok:false, error, code, hint}` on stderr | `internal/output/envelope.go` | Same shape plus `schemaVersion` |
| `--jq` via gojq; `--quiet --jq` runs against `.data` | `internal/cmd/root.go` | Same, `github.com/itchyny/gojq` |
| `--ids-only`, `--count`, `--quiet`, `--styled` | `FormatFromFlags` | Same; `--styled` is called `--plain` here |
| Typed errors with a fixed exit-code table | `internal/apierr`, `internal/output/codes.go` | `internal/clierr`, `help exit-codes` |
| `SilenceUsage`/`SilenceErrors` on root; cobra parse errors normalized into usage errors with a hint | `normalizeCobraError` | Same |
| Per-command `agent_notes` annotation, `commands --json`, hidden `--agent` that turns `--help` into JSON | `internal/cmd/commands.go`, `printAgentHelp` | Same |
| Help topics `help output`, `help exit-codes`, `help environment` | `internal/cmd/help_topics.go` | Same |
| Embedded `SKILL.md`, `hey skill`, `hey skill install` with an ownership marker | `skills/embed.go`, `skill_install.go` | `skills/mail-app-cli/SKILL.md`, `skill`, `skill install` |
| `.surface` snapshot test that fails on removed commands or flags | `surface_test.go`, `check-surface-compat.sh` | `cmd/surface_test.go` + `.surface` |
| Config file with per-key source tracking, precedence flag > env > config > default | `internal/config` | `internal/config`, `config show/set/unset/path` |
| Bare top-level verbs for the hot path (`hey seen 1 2`, `hey trash 3`) over IDs; noun groups for everything else | `internal/cmd/seen.go` etc. | `show`, `archive`, `delete`, `move`, `seen`, `unseen`, `flag`, `unflag`, `inbox`, `unread` |
| Compatibility forms kept and labelled | `Annotations["compatibility_usage"]` | `messages *` group stays, annotated |

What we deliberately do not copy: keyring auth, the `.hey/config.json` trust prompt, `watch` (no push channel from Mail.app), self-update, and the goreleaser pipeline. None of those fit a local Mail.app tool installed via `go install`.

## Current state that shapes the work

- Every message command requires both `-a` and `-m` (`cmd/output.go:26`). There is no default account and no way to act on a message ID without naming its mailbox.
- Flags are package-level globals. `threads archive` swaps `msgAccount`, `msgMailbox`, `batchDryRun`, `batchYes` in and out to reuse `runMessageBatch` (`cmd/threads.go:87-91`).
- Errors always exit 1 and print twice (cobra usage plus `Execute`).
- `--json` exists only on `sync`; `--dry-run` exists on batch, drafts, rules, and threads but not on single `messages archive|delete|move|mark|flag` or `send`.
- Short flags collide: `-s` is `--since` on `messages list` and `--subject` on `send`.
- The Envelope Index already gives us `messages.ROWID`, which is the same number Mail.app returns from `msg.id()`. A message can be located by ID alone with one SQL query (`join mailboxes mb on mb.ROWID = m.mailbox`), which is what makes `archive 123` possible.
- osascript calls are serialized and cost 0.4s to 2s each; index reads cost about 0.1s. Anything that can be answered from the index should be.

## Design

### Output

`internal/output`:

- `Format` is one of `json`, `plain`, `quiet`, `ids`, `count`. Resolution order: `--count` > `--ids-only` > `--quiet` > `--json` / `--jq` / `--agent` > `--plain` > config `output` > env `MAIL_APP_CLI_OUTPUT` > auto. Auto is `plain` when stdout is a terminal, `json` otherwise.
- `Writer` has `Data(v, opts)`, `Mutation(result, humanLine)`, `Error(err)`. Every command ends in exactly one `Data` or `Mutation` call. Nothing else in `cmd/` writes to stdout.
- JSON envelope on stdout:

  ```json
  {
    "ok": true,
    "schemaVersion": 1,
    "data": [...],
    "summary": "12 messages in Klu.ai/INBOX",
    "notice": "Envelope Index unavailable; used Mail.app automation",
    "meta": {"command": "messages list", "count": 12, "account": "Klu.ai", "mailbox": "INBOX", "source": "index", "durationMs": 130}
  }
  ```

  `data` is never `null`: nil slices become `[]`. `Message`, `Account`, `Mailbox`, and `Attachment` get camelCase json tags; `Message.MarshalJSON` normalizes nil recipient slices.
- Errors on stderr: `{"ok": false, "schemaVersion": 1, "error": "...", "code": "not_found", "exitCode": 2, "hint": "..."}`.
- `--jq EXPR` implies JSON, runs gojq over the envelope (or over `data` with `--quiet`), prints string results raw like `jq -r`, and refuses to combine with `--plain`.
- `--ids-only` and `--count` require list data and error otherwise.
- Plain mode: tables through `text/tabwriter`. Messages show `ID`, `DATE`, flags column (`•` unread, `⚑` flagged), `FROM`, `SUBJECT`. Mailboxes show `ACCOUNT`, `MAILBOX`, `UNREAD`, `TOTAL`. `show` prints a header block then the body. Mutations print one line ("Archived 2 messages to All Mail"). Color is ANSI-16 only, disabled by `NO_COLOR`, `--no-color`, or a non-TTY.
- Warnings that `pkg/mail` writes to stderr (index fallback, content budget) stay on stderr; the writer also surfaces them as `notice` when the command can capture them.

### Errors and exit codes

`internal/clierr`:

```go
type Error struct { Code string; Message string; Hint string; Cause error }
```

| Exit | Code | When |
|---|---|---|
| 0 | | success |
| 1 | `usage` | bad flags or arguments, missing required scope, refused flag combinations |
| 2 | `not_found` | message, account, mailbox, rule, draft, signature, attachment |
| 3 | `unavailable` | Mail.app bridge missing, automation permission denied, Envelope Index unreadable when the command needs it |
| 4 | `timeout` | `AutomationTimeoutError`, `AutomationLockTimeoutError` |
| 5 | `partial` | cross-mailbox search incomplete without `--allow-partial` |
| 6 | `mutation_failed` | batch had failures, verification mismatch |
| 7 | `internal` | anything else |

`clierr.Classify(err)` maps existing `pkg/mail` errors onto these codes by type where a type exists and by message where it doesn't (the "not found" strings are stable enough to match; typing them is follow-up work). Root sets `SilenceUsage` and `SilenceErrors`; `Execute` classifies, writes through the writer, and exits with the code. Cobra's own parse errors become `usage` with the hint `run 'mail-app-cli <command> --help'`.

### Scope: account and mailbox

- `--account/-a` and `--mailbox/-m` move to root persistent flags. Every per-command definition goes away.
- `internal/config` reads `~/.config/mail-app-cli/config.json` (respects `XDG_CONFIG_HOME`, overridable with `MAIL_APP_CLI_CONFIG`). Keys: `account`, `mailbox`, `output`. Precedence per key: flag > env (`MAIL_APP_CLI_ACCOUNT`, `MAIL_APP_CLI_MAILBOX`, `MAIL_APP_CLI_OUTPUT`) > config > default. `config show` prints each value with its source.
- `resolveAccount()`: flag/env/config; otherwise, if Mail.app has exactly one account, use it; otherwise a `usage` error naming the accounts and the `config set account` fix.
- `resolveMailbox()`: flag/env/config; default `INBOX`.
- `locateMessage(id)`: when `-a` and `-m` are both set, trust them. Otherwise query the Envelope Index for the message's backing mailbox and account. If the index is unavailable, fall back to the resolved account and mailbox and say so in `notice`.

### Commands

New top-level verbs, all reading IDs positionally and accepting several:

| Command | Does |
|---|---|
| `inbox [--unread] [--limit N]` | unified inbox, or one account with `-a` |
| `unread [--limit N]` | unified unread |
| `show ID` | full message; `--metadata-only` skips the body |
| `seen ID...` / `unseen ID...` | read status |
| `flag ID...` / `unflag ID...` | flagged status |
| `archive ID...` | move to All Mail / Archive |
| `delete ID...` | move to trash |
| `move ID... --to MAILBOX` | move |
| `config show|set KEY VALUE|unset KEY|path` | settings |
| `commands [--json]` | full command tree with flags and agent notes |
| `skill [install|path]` | embedded SKILL.md |
| `version` | version through the writer |
| `help output|exit-codes|environment|agents` | help topics |

Every mutation verb takes `--dry-run`. Explicit IDs do not need `--yes`; selection by `--query`, `--sender`, or `--sender-domain` still does for archive, delete, and move. `send` gains `--dry-run`, which prints the composed message without sending.

The `messages *` group stays as-is so 1.x scripts keep working, annotated `compatibility: true` so `commands --json` and agent help mark it. Its single-message mutations (`messages archive|delete|move|mark|flag`) switch to the shared receipt output and gain `--dry-run`.

Mutation receipt, shared by single verbs, `messages batch`, and `threads archive`:

```json
{
  "action": "archive",
  "dryRun": false,
  "matched": 2, "attempted": 2, "succeeded": 2, "failed": 0, "skipped": 0,
  "items": [
    {"id": "123", "account": "Klu.ai", "sourceMailbox": "INBOX", "targetMailbox": "All Mail", "status": "succeeded"}
  ]
}
```

`runMessageBatch` becomes `runMessageBatch(ctx, opts batchOptions, items []batchItem, mutate)` with no package globals. `threads archive` builds its items directly.

### Agent affordances

- `Annotations["agentNotes"]` on commands where the obvious reading is wrong: which ID goes where, that `search` fails closed, that `delete` is trash not purge, that `archive` on Gmail means All Mail.
- `commands --json` walks the tree: path, use, short, flags with defaults, agent notes, compatibility flag.
- Hidden `--agent`: forces JSON and makes `--help` emit the same per-command record as `commands --json`.
- `skills/mail-app-cli/SKILL.md` embedded with `go:embed`. Frontmatter (`name`, `description`), "Agent invariants" (always `--json`, check `ok`, exit codes, dry-run before any mutation by query, IDs are numeric and stable within a store), a quick-reference table, and the migration note from 1.x. `skill install` writes it to `~/.claude/skills/mail-app-cli/SKILL.md` next to a `.managed-by-mail-app-cli` marker and refuses to overwrite a directory without the marker. `MAIL_APP_CLI_SKILL_DIR` overrides the target.
- `.surface`: sorted list of every command path and flag, generated by `cmd/surface_test.go`. The test fails when the file is stale; `UPDATE_SURFACE=1 go test ./cmd` rewrites it. Reviewers see surface changes as a diff.

### Shell completion

Cobra's `completion` command is already there. `help environment` documents how to install it.

## Work breakdown

1. `pkg/mail`: json tags on models, `Message.MarshalJSON` normalization, `LocateMessage(id)` on the index, `ErrNotFound`-style sentinel wrapping where the "not found" strings originate.
2. `internal/clierr`, `internal/output`, `internal/config` with unit tests (format resolution, envelope shape, jq, precedence).
3. `cmd/root.go`: persistent flags, `PersistentPreRunE` building the writer and scope, `Execute` with classification and exit codes, help topics, `--agent` help.
4. Convert every existing command to the writer. Delete `printJSON`. Delete per-command `-a`/`-m`.
5. Batch engine refactor, then the new top-level verbs on top of it, then `messages *` single mutations on top of it.
6. `config`, `commands`, `skill`, `version` commands; `skills/mail-app-cli/SKILL.md`.
7. `cmd/surface_test.go` and `.surface`; `cmd/root_test.go` executing the root command with buffered output against fixtures where no Mail.app is needed (usage errors, config, commands, jq).
8. README rewrite of the usage, output, and jq sections; `help output` and `help exit-codes` text; `AGENTS.md` note on the surface file.
9. Version 2.0.0 in `cmd/root.go`.

## Validation

- `go build`, `go vet ./...`, `go test ./...`.
- Live, against Mail.app on this machine: `doctor`, `accounts list`, `mailboxes list`, `inbox`, `messages list -a X -m INBOX`, `show ID` with and without `-m`, `search`, `--json`, `--jq`, `--ids-only`, `--count`, `--quiet`, `config set/show`, `commands --json`, `--agent --help`, `skill`, `archive --dry-run`, then a reversible real mutation pair (`flag`/`unflag`, `seen`/`unseen`) and an `archive` followed by `move --to INBOX`.
- Exit codes checked with `echo $?` for a missing message, a bad flag, and a partial search.

## Out of scope for 2.0.0

Reply and reply-all, RFC Message-ID identity, idempotent send, recipient search, and draft reconciliation stay in `plans/tars-real-world-reliability.md` releases 2 through 4. The service-layer extraction (`pkg/mail` option structs and `context.Context` on every call) is phase 0 of the TUI plan; this release only touches the pieces the CLI needs.

## Migration from 1.x

| 1.x | 2.0 |
|---|---|
| `jq '.[].Subject'` | `jq '.data[].subject'` or `--jq '.data[].subject'` |
| bare array output | `--quiet` |
| `messages archive ID -a A -m M` | `archive ID` (still accepts `-a`/`-m`) |
| `messages mark ID --read=false` | `unseen ID` |
| "Message archived" text | receipt JSON, or one line in plain mode |
| exit 1 for everything | see `help exit-codes` |
| `sync --json` | `sync` (global `--json`); the old flag is kept as a hidden alias |
