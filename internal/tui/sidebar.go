package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// listSource is what the list shows: the unified inbox, one mailbox, or a
// search.
type listSource struct {
	unified bool
	account string
	mailbox string
	search  string
}

func (s listSource) label() string {
	switch {
	case s.search != "":
		return "search: " + sanitizeLine(s.search)
	case s.unified:
		return "All inboxes"
	default:
		return sanitizeLine(s.account + " / " + s.mailbox)
	}
}

// showsLocation reports whether rows span mailboxes and need a location column.
func (s listSource) showsLocation() bool {
	return s.unified || s.search != ""
}

type entryKind int

const (
	entryUnified entryKind = iota
	entryHeading
	entryMailbox
)

// sidebarEntry is one row: the unified inbox, an account heading, or a mailbox.
type sidebarEntry struct {
	kind    entryKind
	account string
	mailbox string
	unread  int
}

func (e sidebarEntry) selectable() bool { return e.kind != entryHeading }

func (e sidebarEntry) source() listSource {
	return listSource{unified: e.kind == entryUnified, account: e.account, mailbox: e.mailbox}
}

func (e sidebarEntry) label() string {
	switch e.kind {
	case entryUnified:
		return "All inboxes"
	case entryHeading:
		return e.account
	default:
		return e.mailbox
	}
}

type sidebar struct {
	entries  []sidebarEntry
	cursor   int
	selected int
	width    int
	height   int
	accounts []mail.Account
}

func (s *sidebar) resize(width, height int) {
	s.width, s.height = width, height
}

// setData rebuilds the tree: All inboxes, then each enabled account with
// INBOX first and the rest in Mail's order.
// setData returns true when the selected source changed, for example
// because its mailbox is gone.
func (s *sidebar) setData(accounts []mail.Account, mailboxes []mail.Mailbox) bool {
	s.accounts = accounts
	previous := s.current()
	entries := []sidebarEntry{{kind: entryUnified}}
	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		entries = append(entries, sidebarEntry{kind: entryHeading, account: account.Name})
		var inbox, rest []sidebarEntry
		for _, mb := range mailboxes {
			if mb.Account != account.Name || mb.Name == "" {
				continue
			}
			entry := sidebarEntry{kind: entryMailbox, account: mb.Account, mailbox: mb.Name, unread: mb.UnreadCount}
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
		if entry.source() == previous.source() {
			s.selected = i
		}
	}
	s.cursor = min(s.cursor, max(len(entries)-1, 0))
	return s.current().source() != previous.source()
}

// selectInitial opens the requested scope. An account or mailbox that does
// not match an enabled entry is an error rather than a silent fallback, so
// a typo never lands the user in a different account.
func (s *sidebar) selectInitial(account, mailbox string) error {
	s.selected, s.cursor = 0, 0
	if account == "" {
		return nil
	}
	if mailbox == "" {
		mailbox = "INBOX"
	}
	accountSeen := false
	for i, entry := range s.entries {
		if entry.kind != entryMailbox || !strings.EqualFold(entry.account, account) {
			continue
		}
		accountSeen = true
		if strings.EqualFold(entry.mailbox, mailbox) {
			s.selected, s.cursor = i, i
			return nil
		}
	}
	if !accountSeen {
		return &mail.NotFoundError{Kind: "account", Name: account}
	}
	return fmt.Errorf("mailbox %q not found in %s", mailbox, account)
}

func (s *sidebar) current() sidebarEntry {
	if s.selected < 0 || s.selected >= len(s.entries) {
		return sidebarEntry{kind: entryUnified}
	}
	return s.entries[s.selected]
}

// accountAddresses are every address of the account a message belongs to, for
// reply recipient pruning. Mail.app's first address is retained separately for
// compatibility with older account data, so include it when aliases are absent.
func (s *sidebar) accountAddresses(name string) []string {
	for _, account := range s.accounts {
		if account.Name == name {
			return others(append([]string{account.EmailAddress}, account.EmailAddresses...))
		}
	}
	return nil
}

// isGmail reports whether an account exposes Gmail's All Mail, in which case
// its other user mailboxes are labels rather than folders.
func (s *sidebar) isGmail(account string) bool {
	for _, entry := range s.entries {
		if entry.kind == entryMailbox && entry.account == account && entry.mailbox == "All Mail" {
			return true
		}
	}
	return false
}

// adjustUnread moves a mailbox's unread count by delta, for optimistic
// updates until the next mailbox reload.
func (s *sidebar) adjustUnread(account, mailbox string, delta int) {
	for i := range s.entries {
		e := &s.entries[i]
		if e.kind == entryMailbox && e.account == account && strings.EqualFold(e.mailbox, mailbox) {
			e.unread = max(e.unread+delta, 0)
		}
	}
}

func (s *sidebar) mailboxesFor(account string) []string {
	var names []string
	for _, entry := range s.entries {
		if entry.kind == entryMailbox && entry.account == account {
			names = append(names, entry.mailbox)
		}
	}
	return names
}

func (s *sidebar) move(delta int) {
	for next := s.cursor + delta; next >= 0 && next < len(s.entries); next += delta {
		if s.entries[next].selectable() {
			s.cursor = next
			return
		}
	}
}

// jumpTo selects the nth selectable entry, for the digit shortcuts.
func (s *sidebar) jumpTo(m *model, n int) tea.Cmd {
	count := 0
	for i, entry := range s.entries {
		if !entry.selectable() {
			continue
		}
		if count == n {
			s.cursor = i
			return s.choose(m)
		}
		count++
	}
	return nil
}

func (s *sidebar) choose(m *model) tea.Cmd {
	if s.cursor < 0 || s.cursor >= len(s.entries) || !s.entries[s.cursor].selectable() {
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
		s.cursor = len(s.entries)
		s.move(-1)
	case "enter", "l", "right":
		return s.choose(m)
	case "q":
		return m.requestQuit()
	}
	return nil
}

func (s *sidebar) view(m *model) string {
	offset := max(s.cursor-s.height+1, 0)
	var lines []string
	digit := 0
	for i, entry := range s.entries {
		shortcut := " "
		if entry.selectable() {
			digit++
			if digit <= 9 {
				shortcut = strconv.Itoa(digit)
			}
		}
		if i < offset || i >= offset+s.height {
			continue
		}
		if entry.kind == entryHeading {
			lines = append(lines, m.styles.title.Render(fit(sanitizeLine(entry.label()), s.width)))
			continue
		}
		count := ""
		if entry.unread > 0 {
			count = strconv.Itoa(entry.unread)
		}
		indent := "  "
		if entry.kind == entryUnified {
			indent = ""
		}
		marker := " "
		if i == s.selected {
			marker = "▸"
		}
		// marker + shortcut + indent + label + space + count == width
		label := fit(sanitizeLine(entry.label()), max(s.width-2-len(indent)-1-len(count), 4))
		text := marker + shortcut + indent + label + " " + count
		switch {
		case i == s.cursor && m.focus == focusSidebar:
			text = m.styles.title.Render(text)
		case i == s.selected:
			text = m.styles.active.Render(text)
		case entry.unread > 0:
			text = m.styles.unread.Render(text)
		}
		lines = append(lines, text)
	}
	return block(lines, s.width, s.height)
}
