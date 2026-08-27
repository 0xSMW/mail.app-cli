package tui

import (
	"fmt"
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

type composeDoneMsg struct {
	requestResult
	label string
	draft bool
}

// composeModal is the editor for a new message, reply, or forward. Sending
// happens in a command so the screen stays live.
type composeModal struct {
	mode    composeMode
	account string
	inputs  []textinput.Model
	body    textarea.Model
	focus   int
	sending bool
	status  string
	width   int
	height  int
}

func (m *model) openCompose(mode composeMode) tea.Cmd {
	entry := m.sidebar.current()
	account := entry.account
	var original *mail.Message
	if mode != composeNew {
		current := m.list.current()
		if current == nil {
			return notify("select a message first")
		}
		account = current.Account
		if cached, ok := m.reader.cache[bodyKey(*current)]; ok {
			original = cached
		} else {
			// The body is needed for the quote; fetch it and reopen.
			pending := mode
			m.modal = nil
			return tea.Batch(m.loadBody(), func() tea.Msg { return composeAfterBodyMsg{mode: pending, key: bodyKey(*current)} })
		}
	}
	if account == "" {
		if len(m.sidebar.accounts) == 0 {
			return notify("no account to send from")
		}
		for _, a := range m.sidebar.accounts {
			if a.Enabled {
				account = a.Name
				break
			}
		}
	}
	c := newComposeModal(mode, account, original, m.sidebar.accountEmail(account))
	c.resize(m.width, m.contentHeight())
	m.modal = c
	return c.focusCurrent()
}

type composeAfterBodyMsg struct {
	mode composeMode
	key  string
}

func newComposeModal(mode composeMode, account string, original *mail.Message, ownAddress string) *composeModal {
	c := &composeModal{mode: mode, account: account}
	for _, label := range composeLabels {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = strings.ToLower(label)
		if label == "To" || label == "Cc" || label == "Bcc" {
			in.Placeholder = "addresses, comma separated"
		}
		c.inputs = append(c.inputs, in)
	}
	c.body = textarea.New()
	c.body.Prompt = ""
	c.body.ShowLineNumbers = false
	c.body.Placeholder = "message"
	if original != nil {
		c.prefill(original, ownAddress)
	}
	if mode == composeReply || mode == composeReplyAll {
		c.focus = fieldBody
	}
	if mode == composeForward {
		c.focus = fieldTo
	}
	return c
}

func (c *composeModal) prefill(original *mail.Message, ownAddress string) {
	sender := mail.ParseSender(original.Sender)
	subject := strings.TrimSpace(original.Subject)
	switch c.mode {
	case composeReply, composeReplyAll:
		c.inputs[fieldTo].SetValue(sender.Email)
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}
		if c.mode == composeReplyAll {
			var to []string
			for _, addr := range original.ToRecipients {
				if a := strings.ToLower(strings.TrimSpace(addr)); a != "" && a != ownAddress && a != sender.Email {
					to = append(to, a)
				}
			}
			if len(to) > 0 {
				c.inputs[fieldTo].SetValue(sender.Email + ", " + strings.Join(to, ", "))
			}
			var cc []string
			for _, addr := range original.CcRecipients {
				if a := strings.ToLower(strings.TrimSpace(addr)); a != "" && a != ownAddress {
					cc = append(cc, a)
				}
			}
			c.inputs[fieldCc].SetValue(strings.Join(cc, ", "))
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

func quote(original *mail.Message) string {
	header := fmt.Sprintf("On %s, %s wrote:", formatLongDate(original.DateReceived), strings.TrimSpace(original.Sender))
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, line := range strings.Split(sanitizeBody(original.Content), "\n") {
		b.WriteString("> " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *composeModal) resize(width, height int) {
	c.width, c.height = width, height
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
			c.focus = (c.focus + 1) % (fieldBody + 1)
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

func parseAddressList(value string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (c *composeModal) submit(m *model, draft bool) tea.Cmd {
	to := parseAddressList(c.inputs[fieldTo].Value())
	cc := parseAddressList(c.inputs[fieldCc].Value())
	bcc := parseAddressList(c.inputs[fieldBcc].Value())
	subject := strings.TrimSpace(c.inputs[fieldSubject].Value())
	body := strings.TrimSpace(c.body.Value())
	switch {
	case len(to) == 0:
		c.status = "at least one To address is needed"
		c.focus = fieldTo
		return c.focusCurrent()
	case subject == "" && !draft:
		c.status = "a subject is needed"
		c.focus = fieldSubject
		return c.focusCurrent()
	}
	for _, addr := range append(append(append([]string{}, to...), cc...), bcc...) {
		if !strings.Contains(addr, "@") {
			c.status = "not an address: " + addr
			return nil
		}
	}
	c.sending = true
	c.status = "sending…"
	if draft {
		c.status = "saving draft… (Mail.app takes a few seconds)"
	}
	id, ctx := m.actionLane.begin(m.ctx)
	client := m.client.WithContext(ctx)
	account := c.account
	return func() tea.Msg {
		label := fmt.Sprintf("Sent %q to %s", subject, strings.Join(to, ", "))
		var err error
		if draft {
			_, err = client.CreateDraft(mail.DraftInput{Account: account, Subject: subject, Body: body, To: to, Cc: cc, Bcc: bcc})
			label = "Saved draft " + subject
		} else {
			err = client.SendMessage(account, subject, body, to, cc, bcc, nil)
		}
		return composeDoneMsg{requestResult: requestResult{id, err}, label: label, draft: draft}
	}
}

func (m model) onComposeDone(msg composeDoneMsg) (tea.Model, tea.Cmd) {
	if msg.requestID == m.actionLane.id {
		m.actionLane.finish()
	}
	c, _ := m.modal.(*composeModal)
	if msg.err != nil {
		if c != nil {
			c.sending = false
			c.status = "failed: " + msg.err.Error()
		}
		return m, nil
	}
	m.modal = nil
	m.layout()
	return m, notify(msg.label)
}

func (c *composeModal) view(m *model) string {
	var b strings.Builder
	title := map[composeMode]string{composeNew: "New message", composeReply: "Reply", composeReplyAll: "Reply all", composeForward: "Forward"}[c.mode]
	b.WriteString(m.styles.title.Render(title) + m.styles.muted.Render("  from "+c.account) + "\n\n")
	for i, label := range composeLabels {
		name := pad(label+":", 9)
		if i == c.focus {
			name = m.styles.active.Render(name)
		} else {
			name = m.styles.muted.Render(name)
		}
		b.WriteString(name + c.inputs[i].View() + "\n")
	}
	b.WriteString("\n" + c.body.View() + "\n")
	if c.status != "" {
		style := m.styles.muted
		if strings.HasPrefix(c.status, "failed") || strings.Contains(c.status, "needed") || strings.HasPrefix(c.status, "not an") {
			style = m.styles.error
		}
		b.WriteString("\n" + style.Render(c.status))
	}
	return m.styles.frame.Render(strings.TrimRight(b.String(), "\n"))
}

func (c *composeModal) helpBindings() []helpBinding {
	return []helpBinding{{"tab", "next field"}, {"ctrl+s", "send"}, {"ctrl+d", "save draft"}, {"esc", "discard"}}
}
