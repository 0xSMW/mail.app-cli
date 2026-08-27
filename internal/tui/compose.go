package tui

import (
	"fmt"
	netmail "net/mail"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

type composeMode int

const (
	composeNew composeMode = iota
	composeReply
	composeReplyAll
	composeForward
)

const (
	fieldTo = iota
	fieldCc
	fieldBcc
	fieldSubject
	fieldBody
)

var composeLabels = []string{"To", "Cc", "Bcc", "Subject"}

var composeTitles = map[composeMode]string{composeNew: "New message", composeReply: "Reply", composeReplyAll: "Reply all", composeForward: "Forward"}

type composeDoneMsg struct {
	err   error
	label string
}

// pendingCompose is a reply or forward waiting for the body of the message
// it quotes. It is tied to that message, not to wherever the cursor is when
// the body arrives.
type pendingCompose struct {
	mode composeMode
	key  string
}

// composeModal is the editor for a new message, reply, or forward. Sending
// happens in a queued write so the screen stays live.
type composeModal struct {
	mode    composeMode
	account string
	inputs  []textinput.Model
	body    textarea.Model
	focus   int
	sending bool
	status  string
	problem bool
}

// openCompose opens the editor. Replies and forwards quote the message under
// the cursor and wait for its body when it is not cached yet.
func (m *model) openCompose(mode composeMode) tea.Cmd {
	if mode == composeNew {
		return m.composeFor(mode, nil)
	}
	current := m.list.current()
	if current == nil {
		return notify("select a message first")
	}
	if cached, ok := m.reader.cached(bodyKey(*current)); ok {
		return m.composeFor(mode, cached)
	}
	m.pendingCompose = &pendingCompose{mode: mode, key: bodyKey(*current)}
	return m.loadBody()
}

func (m *model) composeFor(mode composeMode, original *mail.Message) tea.Cmd {
	account := m.sidebar.current().account
	if original != nil {
		account = original.Account
	}
	if account == "" {
		for _, a := range m.sidebar.accounts {
			if a.Enabled {
				account = a.Name
				break
			}
		}
		if account == "" {
			return notify("no account to send from")
		}
	}
	c := newComposeModal(mode, account, original, m.sidebar.accountEmail(account))
	c.resize(m.width, contentHeight(m.height, m.helpView()))
	m.modal = c
	return c.focusCurrent()
}

func newComposeModal(mode composeMode, account string, original *mail.Message, ownAddress string) *composeModal {
	c := &composeModal{mode: mode, account: account}
	for _, label := range composeLabels {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = strings.ToLower(label)
		if label != "Subject" {
			in.Placeholder = "addresses, comma separated"
		}
		c.inputs = append(c.inputs, in)
	}
	c.body = textarea.New()
	c.body.Prompt = ""
	c.body.ShowLineNumbers = false
	c.body.Placeholder = "message"
	// The defaults cap a message at 99 lines, which truncates quoted replies.
	c.body.MaxHeight = 0
	c.body.MaxContentHeight = 0
	if original != nil {
		c.prefill(original, ownAddress)
	}
	if mode == composeReply || mode == composeReplyAll {
		c.focus = fieldBody
	}
	return c
}

func (c *composeModal) prefill(original *mail.Message, ownAddress string) {
	sender := mail.ParseSender(original.Sender)
	subject := strings.TrimSpace(original.Subject)
	switch c.mode {
	case composeReply, composeReplyAll:
		to := []string{sender.Email}
		if c.mode == composeReplyAll {
			to = append(to, others(original.ToRecipients, ownAddress, sender.Email)...)
			c.inputs[fieldCc].SetValue(strings.Join(others(original.CcRecipients, ownAddress, sender.Email), ", "))
		}
		c.inputs[fieldTo].SetValue(strings.Join(to, ", "))
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}
	case composeForward:
		if !strings.HasPrefix(strings.ToLower(subject), "fwd:") {
			subject = "Fwd: " + subject
		}
	}
	c.inputs[fieldSubject].SetValue(subject)
	c.body.SetValue("\n\n" + quote(original))
	c.body.CursorStart()
}

// others lower-cases addresses and drops the ones already covered.
func others(addresses []string, skip ...string) []string {
	var out []string
	for _, addr := range addresses {
		a := strings.ToLower(strings.TrimSpace(addr))
		if a != "" && !slices.Contains(skip, a) {
			out = append(out, a)
		}
	}
	return out
}

func quote(original *mail.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "On %s, %s wrote:\n", formatLongDate(original.DateReceived), strings.TrimSpace(original.Sender))
	for _, line := range strings.Split(sanitizeBody(original.Content), "\n") {
		b.WriteString("> " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *composeModal) resize(width, height int) {
	inner := max(min(width-8, 100), 30)
	for i := range c.inputs {
		c.inputs[i].SetWidth(inner - 10)
	}
	c.body.SetWidth(inner)
	c.body.SetHeight(max(min(height-12, 30), 4))
}

func (c *composeModal) focusCurrent() tea.Cmd {
	for i := range c.inputs {
		c.inputs[i].Blur()
	}
	c.body.Blur()
	if c.focus == fieldBody {
		return c.body.Focus()
	}
	return c.inputs[c.focus].Focus()
}

func (c *composeModal) handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if c.sending {
		return nil, true
	}
	switch msg.String() {
	case "esc":
		return nil, false
	case "tab":
		c.focus = (c.focus + 1) % (fieldBody + 1)
		return c.focusCurrent(), true
	case "shift+tab":
		c.focus = (c.focus + fieldBody) % (fieldBody + 1)
		return c.focusCurrent(), true
	case "ctrl+s":
		return c.submit(m, false), true
	case "ctrl+d":
		return c.submit(m, true), true
	case "enter":
		if c.focus != fieldBody {
			c.focus++
			return c.focusCurrent(), true
		}
	}
	return c.updateInputs(msg), true
}

func (c *composeModal) handleMsg(_ *model, msg tea.Msg) (tea.Cmd, bool) {
	return c.updateInputs(msg), true
}

func (c *composeModal) updateInputs(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if c.focus == fieldBody {
		c.body, cmd = c.body.Update(msg)
		return cmd
	}
	c.inputs[c.focus], cmd = c.inputs[c.focus].Update(msg)
	return cmd
}

// parseAddressList accepts an RFC 5322 list such as `"Doe, Jane" <j@x>, b@y`
// with semicolons allowed as separators, and returns bare addresses. Input
// the parser still rejects is split on separators outside quotes.
func parseAddressList(value string) []string {
	value = strings.TrimSpace(semicolonsToCommas(value))
	if value == "" {
		return nil
	}
	if parsed, err := netmail.ParseAddressList(value); err == nil {
		out := make([]string, 0, len(parsed))
		for _, addr := range parsed {
			out = append(out, strings.ToLower(addr.Address))
		}
		return out
	}
	var out []string
	for _, part := range splitOutsideQuotes(value, ',') {
		if part = strings.TrimSpace(part); part != "" {
			if email := mail.ParseSender(part).Email; email != "" {
				out = append(out, email)
			}
		}
	}
	return out
}

// semicolonsToCommas turns separator semicolons into commas, leaving any
// inside a quoted display name alone.
func semicolonsToCommas(value string) string {
	runes := []rune(value)
	quoted := false
	for i, r := range runes {
		switch {
		case r == '"' && (i == 0 || runes[i-1] != '\\'):
			quoted = !quoted
		case r == ';' && !quoted:
			runes[i] = ','
		}
	}
	return string(runes)
}

// splitOutsideQuotes splits on sep except inside double quotes.
func splitOutsideQuotes(value string, sep rune) []string {
	var parts []string
	var current strings.Builder
	quoted := false
	for i, r := range value {
		switch {
		case r == '"' && (i == 0 || value[i-1] != '\\'):
			quoted = !quoted
			current.WriteRune(r)
		case r == sep && !quoted:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	return append(parts, current.String())
}

func (c *composeModal) fail(field int, status string) tea.Cmd {
	c.status, c.problem = status, true
	c.focus = field
	return c.focusCurrent()
}

func (c *composeModal) submit(m *model, draft bool) tea.Cmd {
	to := parseAddressList(c.inputs[fieldTo].Value())
	cc := parseAddressList(c.inputs[fieldCc].Value())
	bcc := parseAddressList(c.inputs[fieldBcc].Value())
	subject := strings.TrimSpace(c.inputs[fieldSubject].Value())
	body := strings.TrimSpace(c.body.Value())
	switch {
	case len(to) == 0:
		return c.fail(fieldTo, "at least one To address is needed")
	case subject == "" && !draft:
		return c.fail(fieldSubject, "a subject is needed")
	}
	for _, addr := range slices.Concat(to, cc, bcc) {
		if !strings.Contains(addr, "@") {
			return c.fail(c.focus, "not an address: "+addr)
		}
	}
	c.sending, c.problem = true, false
	c.status = "sending…"
	if draft {
		c.status = "saving draft… (Mail.app takes a few seconds)"
	}
	client := m.client.WithContext(m.writeCtx)
	account := c.account
	return m.writes.push(func() tea.Msg {
		if draft {
			_, err := client.CreateDraft(mail.DraftInput{Account: account, Subject: subject, Body: body, To: to, Cc: cc, Bcc: bcc})
			return composeDoneMsg{err: err, label: "Saved draft " + subject}
		}
		err := client.SendMessage(account, subject, body, to, cc, bcc, nil)
		return composeDoneMsg{err: err, label: fmt.Sprintf("Sent %q to %s", subject, strings.Join(to, ", "))}
	})
}

func (m model) onComposeDone(msg composeDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if c, ok := m.modal.(*composeModal); ok {
			c.sending = false
			c.status, c.problem = sanitizeLine("failed: "+msg.err.Error()), true
		}
		return m, nil
	}
	m.modal = nil
	m.layout()
	return m, notify(msg.label)
}

func (c *composeModal) view(m *model) string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render(composeTitles[c.mode]) + m.styles.muted.Render("  from "+c.account) + "\n\n")
	for i, label := range composeLabels {
		name := m.styles.muted.Render(pad(label+":", 9))
		if i == c.focus {
			name = m.styles.active.Render(pad(label+":", 9))
		}
		b.WriteString(name + c.inputs[i].View() + "\n")
	}
	b.WriteString("\n" + c.body.View() + "\n")
	if c.status != "" {
		style := m.styles.muted
		if c.problem {
			style = m.styles.error
		}
		b.WriteString("\n" + style.Render(c.status))
	}
	return m.styles.frame.Render(strings.TrimRight(b.String(), "\n"))
}

func (c *composeModal) helpBindings() []helpBinding {
	return []helpBinding{{"tab", "next field"}, {"ctrl+s", "send"}, {"ctrl+d", "save draft"}, {"esc", "discard"}}
}
