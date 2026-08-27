package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// sidebarEntry is one row: the unified inbox, an account heading, or a mailbox.
type sidebarEntry struct {
	unified bool
	heading bool
	account string
	mailbox string
	unread  int
	total   int
}

func (e sidebarEntry) label() string {
	switch {
	case e.unified:
		return "All inboxes"
	case e.heading:
		return e.account
	default:
		return e.mailbox
	}
}

type sidebar struct {
	entries  []sidebarEntry
	cursor   int
	selected int
	offset   int
	width    int
	height   int
	accounts []mail.Account
}

func newSidebar() sidebar {
	return sidebar{}
}

func (s *sidebar) resize(width, height int) {
	s.width, s.height = width, height
}

// setData rebuilds the tree: All inboxes, then each enabled account with
// INBOX first and the rest in Mail's order.
func (s *sidebar) setData(accounts []mail.Account, mailboxes []mail.Mailbox) {
	s.accounts = accounts
	previous := s.current()
	entries := []sidebarEntry{{unified: true}}
	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		entries = append(entries, sidebarEntry{heading: true, account: account.Name})
		var inbox []sidebarEntry
		var rest []sidebarEntry
		for _, mb := range mailboxes {
			if mb.Account != account.Name || mb.Name == "" {
				continue
			}
			entry := sidebarEntry{account: mb.Account, mailbox: mb.Name, unread: mb.UnreadCount, total: mb.TotalCount}
			if strings.EqualFold(mb.Name, "INBOX") {
				inbox = append(inbox, entry)
			} else {
				rest = append(rest, entry)
			}
		}
		entries = append(entries, inbox...)
		entries = append(entries, rest...)
	}
	s.entries = entries
	s.selected = 0
	for i, entry := range entries {
		if entry.account == previous.account && entry.mailbox == previous.mailbox && entry.unified == previous.unified {
			s.selected = i
		}
	}
	if s.cursor >= len(entries) {
		s.cursor = max(len(entries)-1, 0)
	}
}

func (s *sidebar) selectInitial(account, mailbox string) {
	if account == "" {
		s.selected, s.cursor = 0, 0
		return
	}
	target := mailbox
	if target == "" {
		target = "INBOX"
	}
	for i, entry := range s.entries {
		if !entry.heading && !entry.unified && strings.EqualFold(entry.account, account) && strings.EqualFold(entry.mailbox, target) {
			s.selected, s.cursor = i, i
			return
		}
	}
	s.selected, s.cursor = 0, 0
}

func (s *sidebar) current() sidebarEntry {
	if s.selected < 0 || s.selected >= len(s.entries) {
		return sidebarEntry{unified: true}
	}
	return s.entries[s.selected]
}

// accountEmail is the address of the account a message belongs to, for
// reply-all recipient pruning.
func (s *sidebar) accountEmail(name string) string {
	for _, account := range s.accounts {
		if account.Name == name {
			return strings.ToLower(account.EmailAddress)
		}
	}
	return ""
}

func (s *sidebar) mailboxesFor(account string) []string {
	var names []string
	for _, entry := range s.entries {
		if !entry.heading && !entry.unified && entry.account == account {
			names = append(names, entry.mailbox)
		}
	}
	return names
}

func (s *sidebar) move(delta int) {
	if len(s.entries) == 0 {
		return
	}
	next := s.cursor
	for {
		next += delta
		if next < 0 || next >= len(s.entries) {
			return
		}
		if !s.entries[next].heading {
			s.cursor = next
			return
		}
	}
}

// selectableIndex maps a digit shortcut to the nth selectable entry.
func (s *sidebar) selectableIndex(n int) int {
	count := 0
	for i, entry := range s.entries {
		if entry.heading {
			continue
		}
		if count == n {
			return i
		}
		count++
	}
	return -1
}

func (s *sidebar) jumpTo(m *model, n int) tea.Cmd {
	idx := s.selectableIndex(n)
	if idx < 0 {
		return nil
	}
	s.cursor = idx
	return s.choose(m)
}

func (s *sidebar) choose(m *model) tea.Cmd {
	if s.cursor < 0 || s.cursor >= len(s.entries) || s.entries[s.cursor].heading {
		return nil
	}
	s.selected = s.cursor
	m.list.leaveSearch()
	m.closeReader()
	m.list.clearSelection()
	m.focus = focusList
	return m.reloadList(false)
}

func (s *sidebar) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "j", "down":
		s.move(1)
	case "k", "up":
		s.move(-1)
	case "g", "home":
		s.cursor = 0
	case "G", "end":
		s.cursor = len(s.entries) - 1
		if s.cursor > 0 && s.entries[s.cursor].heading {
			s.move(-1)
		}
	case "enter", "l", "right":
		return s.choose(m)
	case "q":
		return m.requestQuit()
	}
	return nil
}

func (s *sidebar) view(m *model) string {
	inner := s.width
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+s.height {
		s.offset = s.cursor - s.height + 1
	}
	var lines []string
	digit := 0
	for i := 0; i < len(s.entries); i++ {
		entry := s.entries[i]
		shortcut := ""
		if !entry.heading {
			digit++
			if digit <= 9 {
				shortcut = itoa(digit)
			}
		}
		if i < s.offset || i >= s.offset+s.height {
			continue
		}
		var line string
		switch {
		case entry.heading:
			line = m.styles.title.Render(fit(sanitizeLine(entry.label()), inner))
		default:
			count := ""
			if entry.unread > 0 {
				count = itoa(entry.unread)
			}
			indent := "  "
			if entry.unified {
				indent = ""
			}
			// marker + shortcut + indent + label + space + count == inner
			labelWidth := inner - 2 - len(indent) - 1 - len(count)
			label := indent + fit(sanitizeLine(entry.label()), max(labelWidth, 4))
			marker := " "
			if i == s.selected {
				marker = "▸"
			}
			text := marker + pad(shortcut, 1) + label + " " + count
			switch {
			case i == s.cursor && m.focus == focusSidebar:
				line = m.styles.cursor.Render(text)
			case i == s.selected:
				line = m.styles.active.Render(text)
			case entry.unread > 0:
				line = m.styles.unread.Render(text)
			default:
				line = text
			}
		}
		lines = append(lines, fit(line, inner))
	}
	for len(lines) < s.height {
		lines = append(lines, strings.Repeat(" ", inner))
	}
	return strings.Join(lines, "\n")
}
