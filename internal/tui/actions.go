package tui

import (
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
		return m.mutate(targets, mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	case "#":
		m.modal = newConfirmModal("Move "+plural(len(targets), "message")+" to Trash?", func(m *model) tea.Cmd {
			return m.mutate(targets, mail.BatchOptions{Action: "delete"}, mail.DeleteMutator)
		})
	case "m":
		if !sameAccount(targets) {
			return notifyProblem("select messages from one account to move them")
		}
		m.modal = newMailboxPicker("Move to", m.sidebar.mailboxesFor(targets[0].Account), func(m *model, mailbox string) tea.Cmd {
			return m.mutate(targets, mail.BatchOptions{Action: "move", TargetMailbox: mailbox}, mail.MoveMutator(false))
		})
	case "u":
		read := !targets[0].Read
		return m.mutate(targets, mail.BatchOptions{Action: "mark", Read: read}, mail.MarkMutator(read))
	case "!":
		flagged := !targets[0].Flagged
		return m.mutate(targets, mail.BatchOptions{Action: "flag", Flagged: flagged}, mail.FlagMutator(flagged))
	}
	return nil
}

func (m *model) markCurrentRead() tea.Cmd {
	current := m.list.current()
	if current == nil || current.Read {
		return nil
	}
	return m.mutate([]mail.Message{*current}, mail.BatchOptions{Action: "mark", Read: true}, mail.MarkMutator(true))
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

	switch opts.Action {
	case "archive", "delete", "move":
		m.list.remove(keys)
		m.reader.forget(keys)
		if m.reader.open && m.list.current() == nil {
			m.closeReader()
		}
	case "mark":
		apply := func(msg *mail.Message) { msg.Read = opts.Read }
		m.list.update(keys, apply)
		m.touchCached(keys, apply)
	case "flag":
		apply := func(msg *mail.Message) { msg.Flagged = opts.Flagged }
		m.list.update(keys, apply)
		m.touchCached(keys, apply)
	}
	m.list.clearSelection()

	client := m.client.WithContext(m.writeCtx)
	run := m.writes.push(func() tea.Msg {
		result, err := mail.RunBatch(client, opts, items, mutate)
		return mutationDoneMsg{err: err, result: result, opts: opts}
	})
	if m.reader.open && (opts.Action == "archive" || opts.Action == "delete" || opts.Action == "move") {
		return tea.Batch(run, m.requestBody())
	}
	return run
}

func (m *model) touchCached(keys map[string]bool, apply func(*mail.Message)) {
	for key := range keys {
		if cached, ok := m.reader.cached(key); ok {
			apply(cached)
		}
	}
}

func (m model) onMutationDone(msg mutationDoneMsg) (tea.Model, tea.Cmd) {
	result := msg.result
	summary := result.Summary(msg.opts)
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
	// Read and flag toggles were applied on screen already; anything that
	// moved a message needs the index's view of where things are now.
	if msg.opts.Action != "mark" && msg.opts.Action != "flag" || result.Failed > 0 {
		cmds = append(cmds, m.scheduleRefresh())
	}
	return m, tea.Batch(cmds...)
}
