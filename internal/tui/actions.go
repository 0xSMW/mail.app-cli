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

// mutate applies an action optimistically and runs it through the batch
// engine. The list is reconciled from the receipt and refreshed from the
// index shortly after.
func (m *model) mutate(action string, targets []mail.Message, opts mail.BatchOptions, mutate mail.Mutator) tea.Cmd {
	if len(targets) == 0 {
		return nil
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
		if m.reader.open {
			if m.list.current() == nil {
				m.closeReader()
			}
		}
	case "mark":
		m.list.update(keys, func(msg *mail.Message) { msg.Read = opts.Read })
		m.touchCached(keys, func(msg *mail.Message) { msg.Read = opts.Read })
	case "flag":
		m.list.update(keys, func(msg *mail.Message) { msg.Flagged = opts.Flagged })
		m.touchCached(keys, func(msg *mail.Message) { msg.Flagged = opts.Flagged })
	}
	m.list.clearSelection()

	id, ctx := m.actionLane.begin(m.ctx)
	client := m.client
	run := func() tea.Msg {
		result, err := mail.RunBatch(ctx, client, opts, items, mutate)
		return mutationDoneMsg{requestResult: requestResult{id, err}, action: action, result: result, keys: keys}
	}
	var cmds []tea.Cmd
	cmds = append(cmds, run)
	if m.reader.open && (action == "archive" || action == "delete" || action == "move") {
		cmds = append(cmds, m.requestBody())
	}
	return tea.Batch(cmds...)
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
	if msg.requestID == m.actionLane.id {
		m.actionLane.finish()
	}
	summary := receiptSummary(msg.action, msg.result)
	var cmds []tea.Cmd
	if msg.err != nil && msg.result.Attempted == 0 && len(msg.result.Items) == 0 {
		cmds = append(cmds, notifyError(msg.action+" failed", msg.err))
	} else if msg.result.Failed > 0 {
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

func receiptSummary(action string, result mail.BatchResult) string {
	verb := map[string]string{"archive": "Archived", "delete": "Trashed", "move": "Moved", "mark": "Marked", "flag": "Flagged"}[action]
	count := plural(result.Matched, "message")
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
	if result.Failed > 0 {
		return verb + " " + itoa(result.Succeeded) + " of " + count + target + "; " + itoa(result.Failed) + " failed"
	}
	return strings.TrimSpace(verb + " " + count + target)
}
