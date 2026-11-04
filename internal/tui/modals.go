package tui

import (
	"time"

	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
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

// searchScope is how wide a search runs: the selected mailbox, every
// mailbox of its account, or every enabled account.
type searchScope int

const (
	scopeMailbox searchScope = iota
	scopeAccount
	scopeAll
)

// searchScopes lists the scopes the selection offers, in tab order. All
// inboxes spans accounts, so it has no narrower scope to offer.
func searchScopes(entry sidebarEntry) []searchScope {
	if entry.kind != entryMailbox {
		return []searchScope{scopeAll}
	}
	return []searchScope{scopeMailbox, scopeAccount, scopeAll}
}

type searchModal struct {
	inputBox
	entry sidebarEntry
	scope searchScope
}

type searchDoneMsg struct {
	requestResult
	source listSource
	result mail.SearchResult
	silent bool
}

// newSearchModal opens the search box over the current selection. A search
// list already showing seeds the scope the user last chose.
func newSearchModal(entry sidebarEntry, showing listSource) *searchModal {
	sm := &searchModal{inputBox: newInputBox("/ ", "words to find in subject, sender, or summary"), entry: entry}
	sm.scope = searchScopes(entry)[0]
	if showing.search != "" {
		seed := scopeAll
		switch {
		case showing.mailbox != "":
			seed = scopeMailbox
		case showing.account != "":
			seed = scopeAccount
		}
		for _, s := range searchScopes(entry) {
			if s == seed {
				sm.scope = seed
			}
		}
	}
	return sm
}

// cycleScope moves to the next scope the selection offers, wrapping.
func (sm *searchModal) cycleScope() {
	scopes := searchScopes(sm.entry)
	for i, s := range scopes {
		if s == sm.scope {
			sm.scope = scopes[(i+1)%len(scopes)]
			return
		}
	}
	sm.scope = scopes[0]
}

// source is the list source the search runs over: the query plus the
// account and mailbox the scope selects. A scope without a selection to
// name degrades to every account, matching how it is rendered.
func (sm *searchModal) source(query string) listSource {
	src := listSource{search: query}
	switch {
	case sm.scope == scopeMailbox && sm.entry.kind == entryMailbox:
		src.account, src.mailbox = sm.entry.account, sm.entry.mailbox
	case sm.scope == scopeAccount && sm.entry.kind != entryUnified:
		src.account = sm.entry.account
	}
	return src
}

func (sm *searchModal) scopeLabel() string {
	switch {
	case sm.scope == scopeMailbox && sm.entry.kind == entryMailbox:
		return sanitizeLine(sm.entry.account + " / " + sm.entry.mailbox)
	case sm.scope == scopeAccount && sm.entry.kind != entryUnified:
		return sanitizeLine(sm.entry.account) + " (all mailboxes)"
	default:
		return "all accounts"
	}
}

func (sm *searchModal) handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		return nil, false
	case "tab":
		sm.cycleScope()
		return nil, true
	case "enter":
		query := strings.TrimSpace(sm.input.Value())
		if query == "" {
			return nil, false
		}
		return m.runSearch(sm.source(query), false), false
	}
	return sm.update(msg), true
}

func (sm *searchModal) view(m *model) string {
	sm.input.SetWidth(max(min(m.width-10, 70), 20))
	return m.styles.frame.Render(m.styles.title.Render("Search "+sm.scopeLabel()) + "\n" + sm.input.View())
}

func (sm *searchModal) helpBindings() []helpBinding {
	bindings := []helpBinding{{"enter", "search"}}
	if len(searchScopes(sm.entry)) > 1 {
		bindings = append(bindings, helpBinding{"tab", "scope"})
	}
	return append(bindings, helpBinding{"esc", "cancel"})
}

// runSearch searches the scope its source records: a mailbox, an account,
// or every account when both are empty. silent re-runs keep the cursor.
func (m *model) runSearch(src listSource, silent bool) tea.Cmd {
	if !silent {
		m.closeReader()
	}
	if m.writes.busy {
		// Search rows would show what a queued action is changing; the
		// refresh after the queue drains runs the search.
		m.deferRead(src)
		return nil
	}
	m.listLane.abandon()
	id, ctx := m.searchLane.begin(m.ctx, silent)
	client := m.client.WithContext(ctx)
	return func() tea.Msg {
		result, err := client.Search(src.search, src.account, src.mailbox, searchLimit)
		return searchDoneMsg{requestResult: requestResult{id, err}, source: src, result: result, silent: silent}
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
	keepCursor := msg.silent && m.list.source == msg.source
	messages, lagging := m.reconcile(msg.result.Messages, time.Now())
	m.list.setMessages(messages, keepCursor, msg.source, 0)
	if !keepCursor {
		m.list.clearSelection()
	}
	m.focus = focusList
	m.notice = ""
	switch {
	case !msg.result.Complete:
		m.notice = plural(len(msg.result.FailedMailboxes), "mailbox") + " could not be completely searched"
	case len(msg.result.Messages) >= searchLimit:
		m.notice = "showing the newest " + strconv.Itoa(searchLimit) + " matches; narrow the query for older ones"
	}
	cmd = m.syncReader()
	if lagging {
		cmd = tea.Batch(cmd, m.scheduleRefresh())
	}
	return m, cmd
}
