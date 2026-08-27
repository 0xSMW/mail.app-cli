package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

type mutationDoneMsg struct {
	requestResult
	action string
	result mail.BatchResult
	keys   map[string]bool
}

// handleActionKey runs the message actions shared by the list and the reader.
func (m *model) handleActionKey(key string) tea.Cmd {
	targets := m.list.targets()
	switch key {
	case "e":
		return m.mutate("archive", targets, mail.BatchOptions{}, mail.ArchiveMutator(false))
	case "#":
		if len(targets) == 0 {
			return nil
		}
		m.modal = newConfirmModal("Move "+plural(len(targets), "message")+" to Trash?", func(m *model) tea.Cmd {
			return m.mutate("delete", targets, mail.BatchOptions{}, mail.DeleteMutator)
		})
		return nil
	case "m":
		if len(targets) == 0 {
			return nil
		}
		if !sameAccount(targets) {
			return func() tea.Msg {
				return notifyMsg{text: "select messages from one account to move them", kind: toastError}
			}
		}
		account := targets[0].Account
		m.modal = newMailboxPicker(m.styles, "Move to", m.sidebar.mailboxesFor(account), func(m *model, mailbox string) tea.Cmd {
			return m.mutate("move", targets, mail.BatchOptions{TargetMailbox: mailbox}, mail.MoveMutator(false))
		})
		return nil
	case "u":
		if len(targets) == 0 {
			return nil
		}
		read := !targets[0].Read
		return m.mutate("mark", targets, mail.BatchOptions{Read: read}, mail.MarkMutator(read))
	case "!":
		if len(targets) == 0 {
			return nil
		}
		flagged := !targets[0].Flagged
		return m.mutate("flag", targets, mail.BatchOptions{Flagged: flagged}, mail.FlagMutator(flagged))
	case "r":
		return m.openCompose(composeReply)
	case "R":
		return m.openCompose(composeReplyAll)
	case "f":
		return m.openCompose(composeForward)
	}
	return nil
}

func (m *model) markCurrentRead() tea.Cmd {
	current := m.list.current()
	if current == nil || current.Read {
		return nil
	}
	return m.mutate("mark", []mail.Message{*current}, mail.BatchOptions{Read: true}, mail.MarkMutator(true))
}

// writeQueue runs Mail.app writes one after another. A write is never
// cancelled by a later one, unlike reads, because Mail.app may have applied
// it by the time the process is killed.
type writeQueue struct {
	busy    bool
	pending []tea.Cmd
}

func (q *writeQueue) push(cmd tea.Cmd) tea.Cmd {
	if q.busy {
		q.pending = append(q.pending, cmd)
		return nil
	}
	q.busy = true
	return cmd
}

// next starts the next queued write, or marks the queue idle.
func (q *writeQueue) next() tea.Cmd {
	if len(q.pending) == 0 {
		q.busy = false
		return nil
	}
	cmd := q.pending[0]
	q.pending = q.pending[1:]
	return cmd
}

func (m *model) enqueueWrite(cmd tea.Cmd) tea.Cmd {
	return m.writes.push(cmd)
}

// requestQuit leaves once every queued write has finished.
func (m *model) requestQuit() tea.Cmd {
	if !m.writes.busy {
		return tea.Quit
	}
	m.quitting = true
	return notify("finishing Mail.app actions, then quitting")
}

func (m *model) quitIfDrained() tea.Cmd {
	if m.quitting && !m.writes.busy {
		return tea.Quit
	}
	return nil
}

// mutate applies an action optimistically and queues it for Mail.app. The
// list is reconciled from the receipt and refreshed from the index shortly
// after.
func (m *model) mutate(action string, targets []mail.Message, opts mail.BatchOptions, mutate mail.Mutator) tea.Cmd {
	if len(targets) == 0 {
		return nil
	}
	if action == "move" && !sameAccount(targets) {
		return func() tea.Msg {
			return notifyMsg{text: "select messages from one account to move them", kind: toastError}
		}
	}
	opts.Action = action
	keys := make(map[string]bool, len(targets))
	items := make([]mail.BatchItem, 0, len(targets))
	for _, t := range targets {
		keys[bodyKey(t)] = true
		items = append(items, mail.BatchItem{ID: t.ID, Account: t.Account, SourceMailbox: t.Mailbox, Subject: t.Subject})
	}

	switch action {
	case "archive", "delete", "move":
		m.list.remove(keys)
		for key := range keys {
			delete(m.reader.cache, key)
		}
		if m.reader.open && m.list.current() == nil {
			m.closeReader()
		}
	case "mark":
		m.list.update(keys, func(msg *mail.Message) { msg.Read = opts.Read })
		m.touchCached(keys, func(msg *mail.Message) { msg.Read = opts.Read })
	case "flag":
		m.list.update(keys, func(msg *mail.Message) { msg.Flagged = opts.Flagged })
		m.touchCached(keys, func(msg *mail.Message) { msg.Flagged = opts.Flagged })
	}
	m.list.clearSelection()

	client := m.client.WithContext(m.writeCtx)
	ctx := m.writeCtx
	run := func() tea.Msg {
		if action == "archive" {
			items = archiveSources(client, items)
		}
		result, err := mail.RunBatch(ctx, client, opts, items, mutate)
		return mutationDoneMsg{requestResult: requestResult{err: err}, action: action, result: result, keys: keys}
	}
	cmds := []tea.Cmd{m.enqueueWrite(run)}
	if m.reader.open && (action == "archive" || action == "delete" || action == "move") {
		cmds = append(cmds, m.requestBody())
	}
	return tea.Batch(cmds...)
}

func sameAccount(targets []mail.Message) bool {
	for _, t := range targets[1:] {
		if t.Account != targets[0].Account {
			return false
		}
	}
	return true
}

// archiveSources makes archive act from INBOX or the backing mailbox, never
// from a user label, matching the CLI. Messages already in the archive are
// skipped rather than reported as archived.
func archiveSources(client *mail.Client, items []mail.BatchItem) []mail.BatchItem {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if !strings.EqualFold(item.SourceMailbox, "INBOX") {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return items
	}
	located, err := client.LocateMessages(ids)
	if err != nil {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if loc, ok := located[item.ID]; ok && loc.Account == item.Account {
			if mail.IsArchiveAlias(loc.ArchiveMailbox) {
				continue
			}
			item.SourceMailbox = loc.ArchiveMailbox
		}
		out = append(out, item)
	}
	return out
}

func (m *model) touchCached(keys map[string]bool, apply func(*mail.Message)) {
	for key := range keys {
		if cached, ok := m.reader.cache[key]; ok {
			apply(cached)
			if m.reader.message != nil && bodyKey(*m.reader.message) == key {
				m.reader.show(cached, m.styles)
			}
		}
	}
}

func (m model) onMutationDone(msg mutationDoneMsg) (tea.Model, tea.Cmd) {
	summary := receiptSummary(msg.action, msg.result, len(msg.keys))
	cmds := []tea.Cmd{m.writes.next(), m.quitIfDrained()}
	if msg.err != nil && msg.result.Attempted == 0 && len(msg.result.Items) == 0 {
		cmds = append(cmds, notifyError(msg.action+" failed", msg.err))
	} else if msg.result.Failed > 0 || msg.result.Succeeded < len(msg.keys) {
		reason := ""
		for _, item := range msg.result.Items {
			if item.Status == "failed" {
				reason = firstNonEmpty(item.Error, item.VerifyError)
				break
			}
		}
		cmds = append(cmds, func() tea.Msg { return notifyMsg{text: summary + ": " + reason, kind: toastError} })
	} else if msg.action != "mark" {
		cmds = append(cmds, notify(summary))
	}
	cmds = append(cmds, m.scheduleRefresh())
	return m, tea.Batch(cmds...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// receiptSummary describes what happened to the requested messages, which
// can be more than the receipt covers when archive skipped already-archived
// ones.
func receiptSummary(action string, result mail.BatchResult, requested int) string {
	verb := map[string]string{"archive": "Archived", "delete": "Trashed", "move": "Moved", "mark": "Marked", "flag": "Flagged"}[action]
	count := plural(requested, "message")
	target := ""
	destinations := map[string]bool{}
	for _, item := range result.Items {
		if item.TargetMailbox != "" {
			destinations[item.TargetMailbox] = true
		}
	}
	if len(destinations) == 1 && (action == "archive" || action == "move") {
		for name := range destinations {
			target = " to " + name
		}
	}
	if result.Failed > 0 || result.Succeeded < requested {
		summary := verb + " " + itoa(result.Succeeded) + " of " + count + target
		if result.Failed > 0 {
			summary += "; " + itoa(result.Failed) + " failed"
		}
		if skipped := requested - result.Succeeded - result.Failed; skipped > 0 {
			summary += "; " + itoa(skipped) + " skipped"
		}
		return summary
	}
	return strings.TrimSpace(verb + " " + count + target)
}
