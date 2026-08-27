package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

type listMode int

const (
	listMailbox listMode = iota
	listSearch
)

type list struct {
	messages []mail.Message
	cursor   int
	offset   int
	selected map[string]bool
	width    int
	height   int
	mode     listMode
	query    string
	source   sidebarEntry
	partial  int
}

func newList() list {
	return list{selected: map[string]bool{}}
}

func (l *list) resize(width, height int) {
	l.width, l.height = width, height
}

func (l *list) pageSize() int {
	return max(l.height*3, 60)
}

func (l *list) title() string {
	switch {
	case l.mode == listSearch:
		return "search: " + sanitizeLine(l.query) + "  (" + plural(len(l.messages), "match") + ")"
	case l.source.unified:
		return "All inboxes  (" + plural(len(l.messages), "message") + ")"
	default:
		return sanitizeLine(l.source.account+" / "+l.source.mailbox) + "  (" + plural(len(l.messages), "message") + ")"
	}
}

func (l *list) setMessages(messages []mail.Message, keepCursor bool, source sidebarEntry) {
	currentID := ""
	if current := l.current(); current != nil {
		currentID = current.ID
	}
	l.messages = messages
	l.source = source
	if !keepCursor {
		l.cursor, l.offset = 0, 0
		return
	}
	l.cursor = 0
	for i, m := range messages {
		if m.ID == currentID {
			l.cursor = i
		}
	}
	l.clampCursor()
}

func (l *list) clampCursor() {
	if l.cursor >= len(l.messages) {
		l.cursor = len(l.messages) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

func (l *list) current() *mail.Message {
	if l.cursor < 0 || l.cursor >= len(l.messages) {
		return nil
	}
	return &l.messages[l.cursor]
}

func (l *list) jumpToID(id string) bool {
	for i, m := range l.messages {
		if m.ID == id {
			l.cursor = i
			return true
		}
	}
	return false
}

// targets is what an action applies to: the selection when there is one,
// otherwise the message under the cursor.
func (l *list) targets() []mail.Message {
	if len(l.selected) > 0 {
		var out []mail.Message
		for _, m := range l.messages {
			if l.selected[bodyKey(m)] {
				out = append(out, m)
			}
		}
		return out
	}
	if current := l.current(); current != nil {
		return []mail.Message{*current}
	}
	return nil
}

func (l *list) clearSelection() {
	l.selected = map[string]bool{}
}

func (l *list) toggleSelected() {
	if current := l.current(); current != nil {
		key := bodyKey(*current)
		if l.selected[key] {
			delete(l.selected, key)
		} else {
			l.selected[key] = true
		}
	}
}

// remove drops messages by key, keeping the cursor on a neighbour.
func (l *list) remove(keys map[string]bool) {
	kept := l.messages[:0]
	for _, m := range l.messages {
		if !keys[bodyKey(m)] {
			kept = append(kept, m)
		}
	}
	l.messages = kept
	for key := range keys {
		delete(l.selected, key)
	}
	l.clampCursor()
}

func (l *list) update(keys map[string]bool, apply func(*mail.Message)) {
	for i := range l.messages {
		if keys[bodyKey(l.messages[i])] {
			apply(&l.messages[i])
		}
	}
}

func (l *list) enterSearch(query string, messages []mail.Message, failed int) {
	l.mode = listSearch
	l.query = query
	l.partial = failed
	l.messages = messages
	l.cursor, l.offset = 0, 0
	l.clearSelection()
}

func (l *list) leaveSearch() {
	if l.mode != listSearch {
		return
	}
	l.mode = listMailbox
	l.query = ""
	l.messages = nil
	l.cursor, l.offset = 0, 0
	l.clearSelection()
}

func (l *list) move(delta int) {
	if len(l.messages) == 0 {
		return
	}
	l.cursor = min(max(l.cursor+delta, 0), len(l.messages)-1)
}

func (l *list) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "j", "down":
		l.move(1)
		return m.previewIfOpen()
	case "k", "up":
		l.move(-1)
		return m.previewIfOpen()
	case "g", "home":
		l.cursor = 0
		return m.previewIfOpen()
	case "G", "end":
		l.cursor = max(len(l.messages)-1, 0)
		return m.previewIfOpen()
	case "pgdown", "ctrl+d":
		l.move(l.height - 1)
		return m.previewIfOpen()
	case "pgup", "ctrl+u":
		l.move(-(l.height - 1))
		return m.previewIfOpen()
	case "enter", "l", "right":
		return m.openReader()
	case "space":
		l.toggleSelected()
		l.move(1)
	case "esc":
		if l.mode == listSearch {
			l.leaveSearch()
			m.closeReader()
			return m.reloadList(false)
		}
		if len(l.selected) > 0 {
			l.clearSelection()
			return nil
		}
		if m.reader.open {
			m.closeReader()
		}
	case "q":
		if l.mode == listSearch {
			l.leaveSearch()
			m.closeReader()
			return m.reloadList(false)
		}
		return m.requestQuit()
	case "h", "left":
		if m.sidebarVisible() {
			m.focus = focusSidebar
		}
	default:
		return m.handleActionKey(msg.String())
	}
	return nil
}

// previewIfOpen refreshes the reader when it is showing beside the list.
func (m *model) previewIfOpen() tea.Cmd {
	if m.reader.open {
		return m.requestBody()
	}
	return nil
}

func (l *list) view(m *model) string {
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.height {
		l.offset = l.cursor - l.height + 1
	}
	if len(l.messages) == 0 {
		text := "no messages"
		if m.listLane.loading {
			text = "loading…"
		}
		if l.mode == listSearch {
			text = "no matches"
		}
		lines := []string{m.styles.muted.Render(fit(text, l.width))}
		for len(lines) < l.height {
			lines = append(lines, strings.Repeat(" ", l.width))
		}
		return strings.Join(lines, "\n")
	}

	showLocation := l.source.unified || l.mode == listSearch
	dateWidth := 6
	for _, msg := range l.messages {
		if len(formatDate(msg.DateReceived)) > dateWidth {
			dateWidth = 10
			break
		}
	}
	fromWidth := min(max(l.width/4, 12), 26)
	locWidth := 0
	if showLocation {
		locWidth = min(max(l.width/5, 10), 24)
	}
	// marker(1) + flags(2) + date + from + subject + location, with single-space gaps
	subjectWidth := l.width - 1 - 2 - dateWidth - fromWidth - locWidth - 4
	if showLocation {
		subjectWidth--
	}
	subjectWidth = max(subjectWidth, 8)

	var lines []string
	for i := l.offset; i < len(l.messages) && i < l.offset+l.height; i++ {
		msg := l.messages[i]
		marker := " "
		if l.selected[bodyKey(msg)] {
			marker = m.styles.selected.Render("✓")
		} else if i == l.cursor {
			marker = "▸"
		}
		unread := " "
		if !msg.Read {
			unread = "•"
		}
		flag := " "
		if msg.Flagged {
			flag = m.styles.flagged.Render("⚑")
		}
		subject := sanitizeLine(msg.Subject)
		if subject == "" {
			subject = "(no subject)"
		}
		if msg.Snippet != "" && subjectWidth > 40 {
			subject = fit(subject, min(len([]rune(subject)), subjectWidth))
			remaining := subjectWidth - len([]rune(subject)) - 2
			if remaining > 8 {
				subject += " " + m.styles.muted.Render(truncate(sanitizeLine(msg.Snippet), remaining))
			}
		}
		from := fit(sanitizeLine(mail.ParseSender(msg.Sender).Name), fromWidth)
		date := fit(formatDate(msg.DateReceived), dateWidth)
		row := marker + unread + flag + " " + date + " " + from + " " + fit(subject, subjectWidth)
		if showLocation {
			row += " " + m.styles.muted.Render(fit(sanitizeLine(msg.Account+"/"+msg.Mailbox), locWidth))
		}
		switch {
		case i == l.cursor && m.focus == focusList:
			row = m.styles.cursor.Render(row)
		case !msg.Read:
			row = m.styles.unread.Render(row)
		}
		lines = append(lines, fit(row, l.width))
	}
	for len(lines) < l.height {
		lines = append(lines, strings.Repeat(" ", l.width))
	}
	return strings.Join(lines, "\n")
}
