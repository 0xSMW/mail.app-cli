package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

type reader struct {
	open     bool
	message  *mail.Message
	viewport viewport.Model
	cache    map[string]*mail.Message
	width    int
	height   int
	status   string
	styles   styles
}

func newReader() reader {
	return reader{viewport: viewport.New(), cache: map[string]*mail.Message{}}
}

func (r *reader) resize(width, height int) {
	r.width, r.height = width, height
	r.viewport.SetWidth(width)
	r.viewport.SetHeight(height)
	if r.message != nil {
		r.viewport.SetContent(r.render(r.message, r.status))
	}
}

func (r *reader) show(msg *mail.Message, s styles) {
	r.message = msg
	r.status = ""
	r.styles = s
	r.viewport.SetContent(r.render(msg, ""))
	r.viewport.SetYOffset(0)
}

func (r *reader) showPlaceholder(msg *mail.Message, s styles) {
	if msg == nil {
		return
	}
	r.message = msg
	r.status = "loading body…"
	r.styles = s
	r.viewport.SetContent(r.render(msg, r.status))
	r.viewport.SetYOffset(0)
}

func (r *reader) showError(err error, s styles) {
	r.status = sanitizeLine("could not load body: " + err.Error())
	r.styles = s
	if r.message != nil {
		r.viewport.SetContent(r.render(r.message, r.status))
	}
}

func (r *reader) render(msg *mail.Message, status string) string {
	s := r.styles
	width := max(r.width, 20)
	var b strings.Builder
	line := func(label, value string) {
		if value == "" {
			return
		}
		b.WriteString(s.muted.Render(pad(label, 9)) + truncate(sanitizeLine(value), width-10) + "\n")
	}
	b.WriteString(s.title.Render(truncate(sanitizeLine(msg.Subject), width)) + "\n")
	line("From", msg.Sender)
	line("To", strings.Join(msg.ToRecipients, ", "))
	line("Cc", strings.Join(msg.CcRecipients, ", "))
	line("Date", formatLongDate(msg.DateReceived))
	line("In", msg.Account+" / "+msg.Mailbox+"  id "+msg.ID)
	b.WriteString(s.chrome.Render(strings.Repeat("─", width)) + "\n")
	if status != "" {
		b.WriteString(s.muted.Render(status) + "\n")
	}
	if msg.Content != "" {
		for _, l := range strings.Split(sanitizeBody(msg.Content), "\n") {
			if strings.HasPrefix(l, ">") {
				b.WriteString(s.bodyQuote.Render(wrap(l, width)) + "\n")
				continue
			}
			b.WriteString(wrap(l, width) + "\n")
		}
	} else if status == "" {
		b.WriteString(s.muted.Render("(no text content)") + "\n")
	}
	return b.String()
}

// wrap breaks a line at spaces to fit width; long tokens are cut.
func wrap(line string, width int) string {
	if width <= 0 {
		return line
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	var out []string
	current := ""
	for _, word := range words {
		for len([]rune(word)) > width {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			runes := []rune(word)
			out = append(out, string(runes[:width]))
			word = string(runes[width:])
		}
		switch {
		case current == "":
			current = word
		case len([]rune(current))+1+len([]rune(word)) <= width:
			current += " " + word
		default:
			out = append(out, current)
			current = word
		}
	}
	if current != "" {
		out = append(out, current)
	}
	return strings.Join(out, "\n")
}

func (r *reader) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "j", "down":
		r.viewport.ScrollDown(1)
	case "k", "up":
		r.viewport.ScrollUp(1)
	case "pgdown", "ctrl+d", "space":
		r.viewport.PageDown()
	case "pgup", "ctrl+u":
		r.viewport.PageUp()
	case "g", "home":
		r.viewport.GotoTop()
	case "G", "end":
		r.viewport.GotoBottom()
	case "n":
		m.list.move(1)
		return m.requestBody()
	case "p":
		m.list.move(-1)
		return m.requestBody()
	case "esc", "q", "h", "left":
		m.closeReader()
	case "tab":
		m.focus = focusList
	default:
		return m.handleActionKey(msg.String())
	}
	return nil
}

func (r *reader) view(m *model) string {
	if r.message == nil {
		lines := []string{m.styles.muted.Render(fit("select a message", r.width))}
		for len(lines) < r.height {
			lines = append(lines, strings.Repeat(" ", r.width))
		}
		return strings.Join(lines, "\n")
	}
	content := r.viewport.View()
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = fit(lines[i], r.width)
	}
	for len(lines) < r.height {
		lines = append(lines, strings.Repeat(" ", r.width))
	}
	return strings.Join(lines[:r.height], "\n")
}
