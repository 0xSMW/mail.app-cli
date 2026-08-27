package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// --- confirm ---

type confirmModal struct {
	question string
	confirm  func(*model) tea.Cmd
}

func newConfirmModal(question string, confirm func(*model) tea.Cmd) *confirmModal {
	return &confirmModal{question: question, confirm: confirm}
}

func (c *confirmModal) handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "y", "Y", "enter":
		return c.confirm(m), false
	case "n", "N", "esc", "q":
		return nil, false
	}
	return nil, true
}

func (c *confirmModal) handleMsg(*model, tea.Msg) (tea.Cmd, bool) { return nil, false }

func (c *confirmModal) view(m *model) string {
	return m.styles.frame.Render(c.question + "\n\n" + m.styles.muted.Render("y confirm   n cancel"))
}

func (c *confirmModal) helpBindings() []helpBinding {
	return []helpBinding{{"y", "confirm"}, {"n", "cancel"}}
}

// --- a single text input, shared by the picker and the search box ---

type inputBox struct {
	input textinput.Model
}

func newInputBox(prompt, placeholder string) inputBox {
	input := textinput.New()
	input.Prompt = prompt
	input.Placeholder = placeholder
	input.Focus()
	return inputBox{input: input}
}

func (b *inputBox) handleMsg(_ *model, msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	b.input, cmd = b.input.Update(msg)
	return cmd, cmd != nil
}

func (b *inputBox) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	b.input, cmd = b.input.Update(msg)
	return cmd
}

// --- mailbox picker ---

type mailboxPicker struct {
	inputBox
	title   string
	names   []string
	lowered []string
	cursor  int
	choose  func(*model, string) tea.Cmd
	visible []string
}

func newMailboxPicker(title string, names []string, choose func(*model, string) tea.Cmd) *mailboxPicker {
	p := &mailboxPicker{inputBox: newInputBox("› ", "type to filter"), title: title, names: names, choose: choose}
	for _, name := range names {
		p.lowered = append(p.lowered, strings.ToLower(name))
	}
	p.refilter()
	return p
}

func (p *mailboxPicker) refilter() {
	needle := strings.ToLower(strings.TrimSpace(p.input.Value()))
	p.visible = p.visible[:0]
	for i, name := range p.names {
		if needle == "" || strings.Contains(p.lowered[i], needle) {
			p.visible = append(p.visible, name)
		}
	}
	p.cursor = min(p.cursor, max(len(p.visible)-1, 0))
}

func (p *mailboxPicker) handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		return nil, false
	case "enter":
		if p.cursor < len(p.visible) {
			return p.choose(m, p.visible[p.cursor]), false
		}
		return nil, true
	case "down", "ctrl+n":
		p.cursor = min(p.cursor+1, max(len(p.visible)-1, 0))
		return nil, true
	case "up", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
		return nil, true
	}
	cmd := p.update(msg)
	p.refilter()
	return cmd, true
}

const pickerRows = 12

func (p *mailboxPicker) view(m *model) string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render(p.title) + "\n" + p.input.View() + "\n\n")
	start := max(p.cursor-pickerRows+1, 0)
	for i := start; i < len(p.visible) && i < start+pickerRows; i++ {
		// Mailbox and account names come from Mail.app and the server.
		name := truncate(sanitizeLine(p.visible[i]), 40)
		if i == p.cursor {
			b.WriteString(m.styles.title.Render("▸ "+name) + "\n")
		} else {
			b.WriteString("  " + name + "\n")
		}
	}
	if len(p.visible) == 0 {
		b.WriteString(m.styles.muted.Render("  no mailbox matches") + "\n")
	}
	return m.styles.frame.Render(strings.TrimRight(b.String(), "\n"))
}

func (p *mailboxPicker) helpBindings() []helpBinding {
	return []helpBinding{{"type", "filter"}, {"↑/↓", "choose"}, {"enter", "move"}, {"esc", "cancel"}}
}

// --- search ---

type searchModal struct {
	inputBox
}

type searchDoneMsg struct {
	requestResult
	query  string
	result mail.SearchResult
	silent bool
}

func newSearchModal() *searchModal {
	return &searchModal{inputBox: newInputBox("/ ", "words to find in subject, sender, or summary")}
}

func (sm *searchModal) handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		return nil, false
	case "enter":
		query := strings.TrimSpace(sm.input.Value())
		if query == "" {
			return nil, false
		}
		return m.runSearch(query, false), false
	}
	return sm.update(msg), true
}

func (sm *searchModal) view(m *model) string {
	sm.input.SetWidth(max(min(m.width-10, 70), 20))
	scope := "all accounts"
	if entry := m.sidebar.current(); entry.kind != entryUnified {
		scope = sanitizeLine(entry.account)
	}
	return m.styles.frame.Render(m.styles.title.Render("Search "+scope) + "\n" + sm.input.View())
}

func (sm *searchModal) helpBindings() []helpBinding {
	return []helpBinding{{"enter", "search"}, {"esc", "cancel"}}
}

// runSearch searches the account in scope. silent re-runs keep the cursor.
func (m *model) runSearch(query string, silent bool) tea.Cmd {
	account := ""
	if entry := m.sidebar.current(); entry.kind != entryUnified {
		account = entry.account
	}
	if !silent {
		m.closeReader()
	}
	m.listLane.abandon()
	id, ctx := m.searchLane.begin(m.ctx, silent)
	client := m.client.WithContext(ctx)
	return func() tea.Msg {
		result, err := client.Search(query, account, searchLimit)
		return searchDoneMsg{requestResult: requestResult{id, err}, query: query, result: result, silent: silent}
	}
}

// searchLimit bounds a search. The index search has no offset, so the TUI
// asks for a large page and says so when it fills.
const searchLimit = 500

func (m model) onSearchDone(msg searchDoneMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.searchLane.settle(msg.requestResult)
	if !ok {
		return m, cmd
	}
	keepCursor := msg.silent && m.list.source.search == msg.query
	m.list.setMessages(msg.result.Messages, keepCursor, listSource{search: msg.query}, 0)
	if !keepCursor {
		m.list.clearSelection()
	}
	m.focus = focusList
	m.notice = ""
	switch {
	case !msg.result.Complete:
		m.notice = plural(len(msg.result.FailedMailboxes), "mailbox") + " could not be searched"
	case len(msg.result.Messages) >= searchLimit:
		m.notice = "showing the newest " + strconv.Itoa(searchLimit) + " matches; narrow the query for older ones"
	}
	return m, nil
}
