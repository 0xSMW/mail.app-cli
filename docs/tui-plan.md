# TUI plan

Target: `mail-app-cli tui`, a terminal mail client over the same `pkg/mail` client the CLI uses. The model is `hey-cli`'s `internal/tui` (Bubble Tea v2), adapted for a backend where every write and every body fetch is a 0.4s to 2s serialized `osascript` call and every list is a 0.1s SQLite read. This document depends on `docs/cli-ergonomics-plan.md` landing first: the TUI reuses its error types, config, and mutation receipts.

## What the TUI is for

Triage. Open it, see the inbox across accounts, move through it with the keyboard, read a message, archive or flag or mark it, search, occasionally reply or compose. It is not a replacement for Mail.app's settings, rules editor, or account setup. Those stay in the CLI (`rules`, `config`) or in Mail.app itself.

## What hey-cli does that we copy

| hey-cli mechanism | Where it lives there | What we build |
|---|---|---|
| Separate `hey tui` subcommand; bare `hey` never opens it | `internal/cmd/tui.go` | `mail-app-cli tui`; bare invocation prints help |
| Root model routes to a `sectionView` interface (`Init`, `Update`, `View`, `HelpBindings`, `Resize`, `Loading`, `Restyle`) | `internal/tui/section_view.go`, `tui.go` | Same interface, one section to start (`mail`), room for `search` and `drafts` as sections later |
| One `modal` interface for forms and pickers; `handleKey` returns "still open" | `internal/tui/modal.go` | Same |
| Every fetch is a `tea.Cmd` returning a typed msg embedding `requestResult{requestID, err}` | `internal/tui/request_lane.go` | Same; `requestLane[K]` cancels superseded requests via context and drops stale answers |
| Separate counters for infinite-scroll page loads and live refreshes | `content.go` `listPaging` | Same |
| `viewGenerationMsg` stamps drop messages from an old account | `tui.go` | Same, stamped per account switch |
| Hourglass spinner on 50ms ticks, toasts via `notify()` | `loading.go`, `toast.go` | Same |
| ANSI-16 slots only so terminal themes carry through; `NO_COLOR` disables color | `theme.go`, `styles.go` | Same; no TOML theme overlay in the first release |
| Compose as `bubbles` textinput + textarea; the view that owns the SDK sends, the form does not | `compose.go` | Same; sending goes through `mail.Client.SendMessage` or `CreateDraft` |
| `--remote` opens a thread in a running instance over a Unix socket | `open_remote_unix.go` | Follow-up, not first release |
| TUI tests as pure `model.Update(msg)` calls with a `keyPress()` helper and synthetic `...LoadedMsg` | `tui_test.go` | Same |

What we do not copy: Action Cable live updates (Mail.app has no push channel; we poll the index on a timer), Kitty inline images, the Screener, contacts and calendar sections, and Markdown body rendering by default (mail bodies are plain text or HTML; we render text and strip HTML).

## Constraints from the backend

- `osascript` is serialized behind `automationGate` and a cross-process flock. Two TUI actions cannot run concurrently; the UI must queue and show progress rather than block.
- Message lists and counts come from the Envelope Index in about 0.1s and include a `summaries.summary` snippet. The TUI list should be index-backed and never call JXA for listing.
- Bodies need JXA (`GetMessageDetailsJSON`, about 0.4s). Fetch on selection with a debounce so scrolling through a list does not queue dozens of body requests behind the gate.
- Mutations need JXA or AppleScript (0.4s to 2s). Apply optimistically in the model (mark read, remove from list) and reconcile from the receipt; on failure, restore and toast.
- The index can be unavailable (no Full Disk Access). The TUI should start, explain the situation in a banner, and fall back to JXA listing with a "slow mode" notice rather than refusing to run.
- Mail.app itself is the source of truth; the index lags it by a few seconds after a mutation. After an action, refresh from the index after a short delay rather than immediately.

## Phase 0: service layer

Before any TUI code, `pkg/mail` needs an API the TUI and CLI can share without the `cmd/` package in between.

- Option structs replace positional arguments: `ListOptions{Account, Mailbox, Limit, Offset, UnreadOnly, FlaggedOnly, WithContent, Since}`, `SearchOptions` gains `Account`, `Mailbox`, `Limit`, `Since`, `Sender`, `SenderDomain`.
- `context.Context` on every `Client` method that reaches `osascript` or `sqlite3`. `runAutomation` already builds contexts internally; thread the caller's through so a superseded TUI request can cancel a queued subprocess.
- Move out of `cmd/` into `pkg/mail` (or a new `pkg/service`): message-list caching (`cmd/messages.go`), the batch engine (`cmd/batch.go` after the 2.0 refactor), threading (`cmd/threads.go`), sender filters (`cmd/message_filters.go`). The CLI commands become thin adapters.
- The anonymous request structs in `bulk.go` become named types.
- `LocateMessage(id)` from the CLI plan becomes the standard way to turn an ID into an `(account, mailbox)` pair.
- Add `ListMessageSummaries` that returns the index snippet (`summaries.summary`) for list rows.

Acceptance: `cmd/` imports nothing from `internal/tui`, `internal/tui` imports nothing from `cmd/`, and `go test ./pkg/...` covers list, search, and batch with the fake `osascript` from `automation_test.go`.

## Phase 1: read-only browser

`internal/tui`:

- `tui.go`: root model, `sectionView` routing, global keys (`ctrl+c` twice to quit, `?` help bar, `Tab` cycles focus between sidebar and list and reader, `ctrl+r` refresh, `ctrl+a` account picker).
- `mail.go`: the section. Three panes: mailbox sidebar (accounts and their mailboxes with unread counts, from `GetMailboxesJSON`), message list (index-backed, paged, infinite scroll), reader (header block plus body).
- `list.go`: row rendering with the same columns as the CLI plain table (date, flags, from, subject, snippet in dim), selection, `j`/`k`/arrows, `g`/`G`, page keys.
- `reader.go`: header, body wrapped to width, `v` toggles headers, attachments listed at the bottom with `s` to save.
- `request_lane.go`, `loading.go`, `toast.go`, `styles.go`, `theme.go`, `help.go` ported from hey-cli's shapes.
- `cmd/tui.go`: `mail-app-cli tui [--account A] [--mailbox M] [--message ID]`; `runTUI` is a package variable so the command can be tested without opening a terminal.

Keys for this phase: navigation, `Enter` opens reader, `Esc` back, `/` search modal over the current mailbox (index search), `1`..`9` jump to the nth mailbox, `Shift+A`/`Shift+I` jump to All Mail / INBOX.

## Phase 2: actions

- `e` archive, `#` delete, `m` move (mailbox picker modal), `u` toggle read, `!` toggle flag, `space` multi-select, actions apply to the selection.
- Each action builds `batchItem`s and runs them through the shared batch engine in a `tea.Cmd`; the receipt drives the toast ("Archived 3 to All Mail") and the list reconciliation.
- Undo for archive and move within 10 seconds (`z`), implemented as the reverse move.
- Optimistic UI: rows leave the list on action; on a failed receipt they come back with a red toast.
- Refresh from the index 2s after the last mutation.

## Phase 3: compose and reply

- `c` compose, `r` reply, `R` reply-all, `f` forward. The compose modal is `bubbles/v2` textinput for To/Cc/Bcc/Subject and textarea for the body.
- `ctrl+d` saves as draft through `CreateDraft`; `ctrl+s` sends through `SendMessage`. Both run in a `tea.Cmd` and report via toast.
- Reply and reply-all depend on `plans/tars-real-world-reliability.md` R3.2 (thread headers, recipient placement). Until that lands the TUI quotes the original and prefills recipients from the envelope; thread linkage is best-effort.
- Signatures: picker from `ListSignatures`, appended the way `send --signature` does.

## Phase 4: polish

- Threads section using the synthetic subject grouping from `threads list`, clearly marked synthetic.
- `--remote` socket so `mail-app-cli show ID --open` can jump a running TUI to a message.
- Optional TOML theme overlay from `MAIL_APP_CLI_THEME`.
- Search section with account and mailbox scoping and the `--allow-partial` semantics surfaced as a banner.

## Dependencies

- `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`. These need Go 1.24 or newer; `go.mod` moves from 1.21 and `install.sh` and the README say so.
- `github.com/mattn/go-runewidth` for column layout.
- No glamour: bodies are text.

Binary size and startup time should be checked before and after; the TUI packages are only linked when `tui` is built in, so the CLI-only cost is the dependency graph, not runtime.

## Testing

- `internal/tui` tests drive `model.Update` with synthetic messages: `mailboxesLoadedMsg`, `messagesLoadedMsg`, `bodyLoadedMsg`, `mutationDoneMsg`, and key presses. No terminal, no Mail.app.
- `requestLane` tests: a superseded request's result is dropped; cancellation reaches the context.
- `cmd/tui_test.go`: the command parses flags and calls `runTUI` with the resolved scope.
- One manual checklist in this document's companion PR: start with the index available, start with it disabled (`MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX=1`), archive and undo, compose a draft and confirm it appears in Mail.app's Drafts.

## Status (2.1.0)

Phases 0 through 3 shipped in the `tui` command: service layer (`ListOptions`, `Client.WithContext`, `RunBatch`, `GroupThreads`, `FilterBySender` in `pkg/mail`), the three-pane browser, actions with optimistic updates and a two-second index refresh, search, compose, reply, reply all, forward, and draft saving. Not built: undo (Mail.app renumbers moved messages, so the reverse move needs a fresh lookup), the threads section, `--remote`, and theme overlays. Reply threading headers still wait on R3.2.

## Open questions

- Whether to keep the reader in a third pane or open it full-screen on narrow terminals. hey-cli switches on width; we should too, with 120 columns as the threshold.
- How to show Gmail label mailboxes: as children of All Mail, or flat. The index has label membership, so either is possible.
- Whether `delete` in the TUI should be trash (Mail.app `delete`) or a move to Trash; the CLI uses `msg.delete()`, which Mail.app treats as trash for IMAP accounts. Keep parity with the CLI.
