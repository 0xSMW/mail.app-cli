package tui

import (
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

// --- mailbox picker ---

type mailboxPicker struct {
	title   string
	names   []string
	filter  textinput.Model
	cursor  int
	choose  func(*model, string) tea.Cmd
	height  int
	visible []string
}

func newMailboxPicker(s styles, title string, names []string, choose func(*model, string) tea.Cmd) *mailboxPicker {
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "type to filter"
	input.Focus()
	p := &mailboxPicker{title: title, names: names, filter: input, choose: choose, height: 12}
	p.refilter()
	return p
}

func (p *mailboxPicker) refilter() {
	needle := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	p.visible = p.visible[:0]
	for _, name := range p.names {
		if needle == "" || strings.Contains(strings.ToLower(name), needle) {
			p.visible = append(p.visible, name)
		}
	}
	if p.cursor >= len(p.visible) {
		p.cursor = max(len(p.visible)-1, 0)
	}
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
		if p.cursor < len(p.visible)-1 {
			p.cursor++
		}
		return nil, true
	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
		return nil, true
	}
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(msg)
	p.refilter()
	return cmd, true
}

func (p *mailboxPicker) handleMsg(_ *model, msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	p.filter, cmd = p.filter.Update(msg)
	return cmd, cmd != nil
}

func (p *mailboxPicker) view(m *model) string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render(p.title) + "\n")
	b.WriteString(p.filter.View() + "\n\n")
	start := 0
	if p.cursor >= p.height {
		start = p.cursor - p.height + 1
	}
	for i := start; i < len(p.visible) && i < start+p.height; i++ {
		line := "  " + truncate(p.visible[i], 40)
		if i == p.cursor {
			line = m.styles.cursor.Render("▸ " + truncate(p.visible[i], 40))
		}
		b.WriteString(line + "\n")
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
	input textinput.Model
}

type searchDoneMsg struct {
	requestResult
	query  string
	result mail.SearchResult
}

func newSearchModal(s styles) *searchModal {
	input := textinput.New()
	input.Prompt = "/ "
	input.Placeholder = "words to find in subject, sender, or summary"
	input.Focus()
	return &searchModal{input: input}
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
		return m.runSearch(query), false
	}
	var cmd tea.Cmd
	sm.input, cmd = sm.input.Update(msg)
	return cmd, true
}

func (sm *searchModal) handleMsg(_ *model, msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	sm.input, cmd = sm.input.Update(msg)
	return cmd, cmd != nil
}

func (sm *searchModal) view(m *model) string {
	sm.input.SetWidth(max(min(m.width-10, 70), 20))
	scope := "all accounts"
	if entry := m.sidebar.current(); !entry.unified {
		scope = entry.account
	}
	return m.styles.frame.Render(m.styles.title.Render("Search "+scope) + "\n" + sm.input.View())
}

func (sm *searchModal) helpBindings() []helpBinding {
	return []helpBinding{{"enter", "search"}, {"esc", "cancel"}}
}

func (m *model) runSearch(query string) tea.Cmd {
	account := ""
	if entry := m.sidebar.current(); !entry.unified {
		account = entry.account
	}
	m.closeReader()
	id, ctx := m.listLane.begin(m.ctx)
	client := m.client
	return func() tea.Msg {
		result, err := client.Search(ctx, query, account, "", 100)
		return searchDoneMsg{requestResult: requestResult{id, err}, query: query, result: result}
	}
}

func (m model) onSearchDone(msg searchDoneMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.listLane.settle(msg.requestResult)
	if !ok {
		return m, cmd
	}
	m.list.enterSearch(msg.query, msg.result.Messages, len(msg.result.FailedMailboxes))
	m.focus = focusList
	if !msg.result.Complete {
		m.notice = plural(len(msg.result.FailedMailboxes), "mailbox") + " could not be searched"
	} else {
		m.notice = ""
	}
	return m, nil
}
