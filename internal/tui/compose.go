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
	c := newComposeModal(mode, account, original, m.sidebar.accountAddresses(account))
	c.resize(m.width, contentHeight(m.height, m.helpView()))
	m.modal = c
	return c.focusCurrent()
}

func newComposeModal(mode composeMode, account string, original *mail.Message, ownAddresses []string) *composeModal {
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
		c.prefill(original, ownAddresses)
	}
	if mode == composeReply || mode == composeReplyAll {
		c.focus = fieldBody
	}
	return c
}

func (c *composeModal) prefill(original *mail.Message, ownAddresses []string) {
	senderAddress := senderAddressOf(original.Sender)
	subject := strings.TrimSpace(original.Subject)
	switch c.mode {
	case composeReply, composeReplyAll:
		to := []string{senderAddress}
		if containsFold(ownAddresses, senderAddress) && len(original.ToRecipients) > 0 {
			// Replying to a message the user sent goes back to its recipients.
			to = others(original.ToRecipients, ownAddresses...)
		}
		if c.mode == composeReplyAll {
			covered := append(append([]string(nil), ownAddresses...), to...)
			to = append(to, others(original.ToRecipients, covered...)...)
			covered = append(append([]string(nil), ownAddresses...), to...)
			c.inputs[fieldCc].SetValue(strings.Join(others(original.CcRecipients, covered...), ", "))
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

// senderAddressOf extracts the address from a From header as written,
// keeping the local part's case.
func senderAddressOf(header string) string {
	if parsed, err := netmail.ParseAddress(strings.TrimSpace(header)); err == nil {
		return parsed.Address
	}
	return mail.ParseSender(header).Email
}

func containsFold(values []string, target string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), target) })
}

// others drops skipped or duplicate entries, comparing case-insensitively
// but keeping each address as written.
func others(addresses []string, skip ...string) []string {
	seen := make(map[string]bool, len(addresses)+len(skip))
	for _, addr := range skip {
		if key := strings.ToLower(strings.TrimSpace(addr)); key != "" {
			seen[key] = true
		}
	}
	var out []string
	for _, addr := range addresses {
		a := strings.TrimSpace(addr)
		key := strings.ToLower(a)
		if a != "" && !seen[key] {
			out = append(out, a)
			seen[key] = true
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
// the parser rejects is split on separators outside quotes, and any
// non-empty part that is not an address is an error rather than dropped.
func parseAddressList(value string) ([]string, error) {
	value = strings.TrimSpace(semicolonsToCommas(value))
	if value == "" {
		return nil, nil
	}
	if parsed, err := netmail.ParseAddressList(value); err == nil {
		out := make([]string, 0, len(parsed))
		for _, addr := range parsed {
			out = append(out, addr.Address)
		}
		return out, nil
	}
	var out []string
	for _, part := range splitOutsideQuotes(value, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		email := senderAddressOf(part)
		if email == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("not an address: %s", part)
		}
		out = append(out, email)
	}
	return out, nil
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
	lists := make([][]string, fieldSubject)
	for field := fieldTo; field < fieldSubject; field++ {
		parsed, err := parseAddressList(c.inputs[field].Value())
		if err != nil {
			return c.fail(field, err.Error())
		}
		lists[field] = parsed
	}
	to, cc, bcc := lists[fieldTo], lists[fieldCc], lists[fieldBcc]
	subject := strings.TrimSpace(c.inputs[fieldSubject].Value())
	body := strings.TrimSpace(c.body.Value())
	switch {
	case len(to) == 0:
		return c.fail(fieldTo, "at least one To address is needed")
	case subject == "" && !draft:
		return c.fail(fieldSubject, "a subject is needed")
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
