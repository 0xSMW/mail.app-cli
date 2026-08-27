package tui

import (
	"time"

	"cmp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// writeQueue runs Mail.app writes one after another. A write is never
// cancelled by a later one, unlike reads, because Mail.app may have applied
// it by the time the process is killed. Each queued command is wrapped so
// its result arrives as a writeDoneMsg, which is where the queue advances.
type writeQueue struct {
	busy    bool
	pending []tea.Cmd
}

type writeDoneMsg struct{ inner tea.Msg }

func (q *writeQueue) push(cmd tea.Cmd) tea.Cmd {
	wrapped := func() tea.Msg { return writeDoneMsg{inner: cmd()} }
	if q.busy {
		q.pending = append(q.pending, wrapped)
		return nil
	}
	q.busy = true
	return wrapped
}

func (q *writeQueue) next() tea.Cmd {
	if len(q.pending) == 0 {
		q.busy = false
		return nil
	}
	cmd := q.pending[0]
	q.pending = q.pending[1:]
	return cmd
}

// requestQuit leaves once every queued write has finished.
func (m *model) requestQuit() tea.Cmd {
	if !m.writes.busy {
		return tea.Quit
	}
	m.quitting = true
	return notify("finishing Mail.app actions, then quitting")
}

type mutationDoneMsg struct {
	err    error
	result mail.BatchResult
	opts   mail.BatchOptions
	keys   map[string]bool
	// removed is the rows the optimistic update took off the screen.
	removed map[string]bool
}

// handleActionKey runs the message actions shared by the list and the reader.
func (m *model) handleActionKey(key string) tea.Cmd {
	switch key {
	case "r":
		return m.openCompose(composeReply)
	case "R":
		return m.openCompose(composeReplyAll)
	case "f":
		return m.openCompose(composeForward)
	}
	targets := m.list.targets()
	if len(targets) == 0 {
		return nil
	}
	switch key {
	case "e":
		return m.act(targets, mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	case "#":
		m.modal = newConfirmModal("Move "+plural(len(targets), "message")+" to Trash?", func(m *model) tea.Cmd {
			return m.act(targets, mail.BatchOptions{Action: "delete"}, mail.DeleteMutator)
		})
	case "m":
		if !sameAccount(targets) {
			return notifyProblem("select messages from one account to move them")
		}
		m.modal = newMailboxPicker("Move to", m.sidebar.mailboxesFor(targets[0].Account), func(m *model, mailbox string) tea.Cmd {
			return m.act(targets, mail.BatchOptions{Action: "move", TargetMailbox: mailbox}, mail.MoveMutator(false))
		})
	case "u":
		read := !targets[0].Read
		return m.act(targets, mail.BatchOptions{Action: "mark", Read: read}, mail.MarkMutator(read))
	case "!":
		flagged := !targets[0].Flagged
		return m.act(targets, mail.BatchOptions{Action: "flag", Flagged: flagged}, mail.FlagMutator(flagged))
	}
	return nil
}

// act is a user-requested action: it consumes the selection it applied to.
// Automatic writes (marking read on open) use mutate directly and leave the
// selection alone.
func (m *model) act(targets []mail.Message, opts mail.BatchOptions, mutate mail.Mutator) tea.Cmd {
	m.list.clearSelection()
	if opts.Action == "mark" {
		keys := make(map[string]bool, len(targets))
		for _, t := range targets {
			keys[bodyKey(t)] = true
		}
		if opts.Read {
			// An explicit mark-read lifts any guard.
			for key := range keys {
				delete(m.noAutoRead, key)
			}
		} else if m.reader.open && keys[m.readerKey] {
			// An explicit mark-unread of the message on screen must survive
			// the reader's own marking; rows not being read need no guard.
			m.suppressAutoRead(map[string]bool{m.readerKey: true})
		}
	}
	return m.mutate(targets, opts, mutate)
}

func (m *model) markCurrentRead() tea.Cmd {
	current := m.list.current()
	if current == nil || current.Read {
		return nil
	}
	return m.mutate([]mail.Message{*current}, mail.BatchOptions{Action: "mark", Read: true}, mail.MarkMutator(true))
}

// archiveRemovesFrom reports whether archiving takes the message out of the
// mailbox it is listed under. On Gmail only INBOX and the special folders
// (Trash, Spam, Sent, Drafts) are real locations; anything else is a label
// the message keeps.
func (m *model) archiveRemovesFrom(t mail.Message) bool {
	switch {
	case strings.EqualFold(t.Mailbox, "INBOX"):
		return true
	case mail.IsArchiveAlias(t.Mailbox):
		return false
	case !m.sidebar.isGmail(t.Account):
		return true
	}
	for _, kind := range []string{"trash", "junk", "sent", "drafts"} {
		if mail.IsSpecialMailboxName(kind, t.Mailbox) {
			return true
		}
	}
	return false
}

func sameAccount(targets []mail.Message) bool {
	for _, t := range targets[1:] {
		if t.Account != targets[0].Account {
			return false
		}
	}
	return true
}

// mutate applies an action optimistically and queues it for Mail.app. The
// list is reconciled from the receipt and refreshed from the index shortly
// after.
func (m *model) mutate(targets []mail.Message, opts mail.BatchOptions, mutate mail.Mutator) tea.Cmd {
	keys := make(map[string]bool, len(targets))
	items := make([]mail.BatchItem, 0, len(targets))
	for _, t := range targets {
		keys[bodyKey(t)] = true
		items = append(items, mail.BatchItem{ID: t.ID, Account: t.Account, SourceMailbox: t.Mailbox, Subject: t.Subject})
	}

	notice := m.deferPendingReads()
	removed := map[string]bool{}
	switch opts.Action {
	case "archive", "delete", "move":
		// Rows only leave the screen when the action removes them from the
		// mailbox being viewed: a move to the current mailbox is a no-op;
		// an archive from an archive mailbox is one too, and on Gmail an
		// archive from a label leaves the label attached (the engine acts
		// from INBOX or All Mail), while a plain IMAP folder loses the row.
		moving := make(map[string]bool, len(targets))
		for _, t := range targets {
			if opts.Action == "move" && strings.EqualFold(t.Mailbox, opts.TargetMailbox) {
				continue
			}
			if opts.Action == "archive" && !m.archiveRemovesFrom(t) {
				continue
			}
			moving[bodyKey(t)] = true
			if !t.Read {
				m.sidebar.adjustUnread(t.Account, t.Mailbox, -1)
				if opts.Action == "move" {
					m.sidebar.adjustUnread(t.Account, opts.TargetMailbox, 1)
				}
			}
		}
		if m.list.source.search != "" && opts.Action != "delete" {
			// Search results span mailboxes; an archive or move keeps the
			// message matching, so rows stay until the search re-runs. A
			// delete stops it matching, so its row goes now.
			moving = map[string]bool{}
		}
		removed = moving
		m.list.remove(moving)
		m.reader.forget(moving)
		if m.reader.open && m.list.current() == nil {
			m.closeReader()
		}
	case "mark":
		for _, t := range targets {
			switch {
			case t.Read && !opts.Read:
				m.sidebar.adjustUnread(t.Account, t.Mailbox, 1)
			case !t.Read && opts.Read:
				m.sidebar.adjustUnread(t.Account, t.Mailbox, -1)
			}
		}
		apply := func(msg *mail.Message) { msg.Read = opts.Read }
		m.list.update(keys, apply)
		m.touchCached(keys, apply)
	case "flag":
		apply := func(msg *mail.Message) { msg.Flagged = opts.Flagged }
		m.list.update(keys, apply)
		m.touchCached(keys, apply)
	}

	client := m.client.WithContext(m.writeCtx)
	run := m.writes.push(func() tea.Msg {
		result, err := mail.RunBatch(client, opts, items, mutate)
		return mutationDoneMsg{err: err, result: result, opts: opts, keys: keys, removed: removed}
	})
	if m.reader.open && (opts.Action == "archive" || opts.Action == "delete" || opts.Action == "move") {
		return tea.Batch(run, m.requestBody(), notice)
	}
	return tea.Batch(run, notice)
}

// deferPendingReads drops index reads still in flight when a write starts:
// their answers predate the write and would overwrite the optimistic state.
// The refresh that follows the drained queue reads the settled index. A
// search the user is still waiting on is dropped too, with a notice, since
// its rows would replace the list the action was taken on.
func (m *model) deferPendingReads() tea.Cmd {
	var notice tea.Cmd
	if m.searchLane.inFlight() && m.list.source.search == "" {
		notice = notify("search cancelled by the action; search again")
	}
	for _, lane := range []*requestLane{&m.mailboxLane, &m.listLane, &m.pageLane, &m.searchLane} {
		if lane.inFlight() {
			lane.abandon()
			m.refreshWanted = true
		}
	}
	if m.refreshWanted {
		m.reloadAfterMailboxes = false
	}
	return notice
}

func (m *model) touchCached(keys map[string]bool, apply func(*mail.Message)) {
	for key := range keys {
		if cached, ok := m.reader.cached(key); ok {
			apply(cached)
		}
	}
}

// failedKeys is the cache keys of the items a run did not succeed on; with
// no receipt items at all, every requested key.
func failedKeys(result mail.BatchResult, requested map[string]bool) map[string]bool {
	if len(result.Items) == 0 {
		return requested
	}
	failed := map[string]bool{}
	for _, item := range result.Items {
		if item.Status == "failed" {
			failed[item.Account+"\x00"+item.SourceMailbox+"\x00"+item.ID] = true
		}
	}
	return failed
}

func (m model) onMutationDone(msg mutationDoneMsg) (tea.Model, tea.Cmd) {
	result := msg.result
	summary := result.Summary(msg.opts)
	// Items that failed (or every item, when the run never produced a
	// receipt) may be showing an optimistic state Mail.app refused: drop
	// their cached copies so a reopen fetches the truth, and stop the
	// reader from re-arming a mark on them.
	if failed := failedKeys(result, msg.keys); len(failed) > 0 {
		m.reader.forget(failed)
		if msg.opts.Action == "mark" {
			m.suppressAutoRead(failed)
		}
	}
	m.recordSettled(msg, time.Now())
	var cmds []tea.Cmd
	switch {
	case msg.err != nil && len(result.Items) == 0:
		cmds = append(cmds, notifyError(msg.opts.Action+" failed", msg.err))
	case result.Failed > 0 || result.Skipped > 0:
		reason := ""
		for _, item := range result.Items {
			if item.Status != "succeeded" {
				reason = cmp.Or(item.Error, item.VerifyError)
				break
			}
		}
		cmds = append(cmds, notifyProblem(strings.TrimSpace(summary+": "+reason)))
	case msg.opts.Action != "mark":
		cmds = append(cmds, notify(summary))
	}
	// Every write is reconciled from the index once the queue drains: rows
	// for moves, unread counts for read changes, and anything a failure
	// left optimistic.
	m.refreshWanted = true
	return m, tea.Batch(cmds...)
}
