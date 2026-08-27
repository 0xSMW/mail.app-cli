package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

type list struct {
	messages  []mail.Message
	cursor    int
	selected  map[string]bool
	width     int
	height    int
	source    listSource
	dateWidth int
	// hasMore is true while the last page came back full, so older messages
	// may still be waiting in the mailbox.
	hasMore bool
}

func (l *list) resize(width, height int) {
	l.width, l.height = width, height
}

func (l *list) pageSize() int {
	return max(l.height*3, 60)
}

func (l *list) title() string {
	noun := "message"
	if l.source.search != "" {
		noun = "match"
	}
	return l.source.label() + "  (" + plural(len(l.messages), noun) + ")"
}

func (l *list) setMessages(messages []mail.Message, keepCursor bool, source listSource) {
	currentID := ""
	if current := l.current(); current != nil {
		currentID = current.ID
	}
	l.messages = messages
	l.source = source
	l.hasMore = source.search == "" && len(messages) >= l.pageSize()
	l.cursor = 0
	if keepCursor {
		for i, m := range messages {
			if m.ID == currentID {
				l.cursor = i
			}
		}
	}
	l.measureDates()
	if l.selected == nil {
		l.selected = map[string]bool{}
	}
}

// appendPage adds an older page. Rows the list already has (the index can
// shift while paging) are dropped.
func (l *list) appendPage(messages []mail.Message, pageSize int) {
	seen := make(map[string]bool, len(l.messages))
	for _, m := range l.messages {
		seen[bodyKey(m)] = true
	}
	for _, m := range messages {
		if !seen[bodyKey(m)] {
			l.messages = append(l.messages, m)
		}
	}
	l.hasMore = len(messages) >= pageSize
	l.measureDates()
}

// nearEnd reports whether the cursor is close enough to the last loaded row
// that the next page should be fetched.
func (l *list) nearEnd() bool {
	return l.hasMore && l.cursor >= len(l.messages)-l.height
}

// measureDates picks the date column width: old years need the full date,
// this year fits "Jan 02".
func (l *list) measureDates() {
	l.dateWidth = 6
	now := time.Now()
	for _, m := range l.messages {
		if len(formatDate(m.DateReceived, now)) > 6 {
			l.dateWidth = 10
			break
		}
	}
}

func (l *list) clampCursor() {
	l.cursor = min(max(l.cursor, 0), max(len(l.messages)-1, 0))
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

func (l *list) enterSearch(query string, messages []mail.Message) {
	l.setMessages(messages, false, listSource{search: query})
	l.clearSelection()
}

func (l *list) leaveSearch() {
	if l.source.search == "" {
		return
	}
	l.setMessages(nil, false, listSource{})
	l.clearSelection()
}

func (l *list) move(delta int) {
	l.cursor += delta
	l.clampCursor()
}

func (l *list) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "j", "down":
		l.move(1)
	case "k", "up":
		l.move(-1)
	case "g", "home":
		l.cursor = 0
	case "G", "end":
		l.cursor = len(l.messages) - 1
		l.clampCursor()
	case "pgdown", "ctrl+d":
		l.move(l.height - 1)
	case "pgup", "ctrl+u":
		l.move(-(l.height - 1))
	case "enter", "l", "right":
		return m.openReader()
	case "space":
		l.toggleSelected()
		l.move(1)
		return nil
	case "esc":
		switch {
		case l.source.search != "":
			l.leaveSearch()
			m.closeReader()
			return m.reloadList(false)
		case len(l.selected) > 0:
			l.clearSelection()
		case m.reader.open:
			m.closeReader()
		}
		return nil
	case "q":
		if l.source.search != "" {
			l.leaveSearch()
			m.closeReader()
			return m.reloadList(false)
		}
		return m.requestQuit()
	case "h", "left":
		if m.sidebarVisible() {
			m.focus = focusSidebar
		}
		return nil
	default:
		return m.handleActionKey(msg.String())
	}
	// A cursor move refreshes the reader when it is showing beside the list,
	// and pulls the next page when it nears the end of what is loaded.
	var cmds []tea.Cmd
	if m.reader.open {
		cmds = append(cmds, m.requestBody())
	}
	cmds = append(cmds, m.loadMore())
	return tea.Batch(cmds...)
}

func (l *list) view(m *model) string {
	if len(l.messages) == 0 {
		text := "no messages"
		switch {
		case m.listLane.loading || m.searchLane.loading:
			text = "loading…"
		case l.source.search != "":
			text = "no matches"
		}
		return block([]string{m.styles.muted.Render(text)}, l.width, l.height)
	}

	showLocation := l.source.showsLocation()
	fromWidth := min(max(l.width/4, 12), 26)
	locWidth := 0
	if showLocation {
		locWidth = min(max(l.width/5, 10), 24) + 1
	}
	// marker(1) + flags(2) + gaps + date + from + subject + location
	subjectWidth := max(l.width-1-2-l.dateWidth-fromWidth-locWidth-4, 8)
	offset := max(l.cursor-l.height+1, 0)
	now := time.Now()

	var lines []string
	for i := offset; i < len(l.messages) && i < offset+l.height; i++ {
		msg := l.messages[i]
		marker := " "
		if l.selected[bodyKey(msg)] {
			marker = m.styles.active.Render("✓")
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
		if snippet := sanitizeLine(msg.Snippet); snippet != "" && subjectWidth > 40 {
			subject = truncate(subject, subjectWidth)
			if remaining := subjectWidth - len([]rune(subject)) - 1; remaining > 8 {
				subject += " " + m.styles.muted.Render(truncate(snippet, remaining))
			}
		}
		row := marker + unread + flag + " " +
			fit(formatDate(msg.DateReceived, now), l.dateWidth) + " " +
			fit(sanitizeLine(mail.ParseSender(msg.Sender).Name), fromWidth) + " " +
			fit(subject, subjectWidth)
		if showLocation {
			row += " " + m.styles.muted.Render(fit(sanitizeLine(msg.Account+"/"+msg.Mailbox), locWidth-1))
		}
		switch {
		case i == l.cursor && m.focus == focusList:
			row = m.styles.title.Render(row)
		case !msg.Read:
			row = m.styles.unread.Render(row)
		}
		lines = append(lines, row)
	}
	return block(lines, l.width, l.height)
}
