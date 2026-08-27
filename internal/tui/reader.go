package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// bodyCacheSize bounds the fetched bodies kept in memory.
const bodyCacheSize = 64

type reader struct {
	open     bool
	message  *mail.Message
	viewport viewport.Model
	cache    map[string]*mail.Message
	order    []string
	width    int
	height   int
	status   string
	styles   styles
}

func newReader(s styles) reader {
	return reader{viewport: viewport.New(), cache: map[string]*mail.Message{}, styles: s}
}

func (r *reader) cached(key string) (*mail.Message, bool) {
	msg, ok := r.cache[key]
	return msg, ok
}

// remember keeps a fetched body, evicting the oldest beyond bodyCacheSize.
// The body on screen is spared by moving it to the back of the order, so
// it stays eligible for eviction later.
func (r *reader) remember(key string, msg *mail.Message) {
	if _, ok := r.cache[key]; !ok {
		r.order = append(r.order, key)
	}
	r.cache[key] = msg
	for len(r.order) > bodyCacheSize {
		oldest := r.order[0]
		r.order = r.order[1:]
		if r.message != nil && bodyKey(*r.message) == oldest {
			r.order = append(r.order, oldest)
			continue
		}
		delete(r.cache, oldest)
	}
}

func (r *reader) forget(keys map[string]bool) {
	for key := range keys {
		delete(r.cache, key)
	}
}

// syncFlags copies read and flagged state from freshly listed rows onto
// cached bodies, so a failed mutation's optimistic change is undone.
func (r *reader) syncFlags(messages []mail.Message) {
	for _, m := range messages {
		if cached, ok := r.cache[bodyKey(m)]; ok {
			cached.Read, cached.Flagged = m.Read, m.Flagged
		}
	}
}

func (r *reader) resize(width, height int) {
	rewrap := width != r.width
	r.width, r.height = width, height
	r.viewport.SetWidth(width)
	r.viewport.SetHeight(height)
	if rewrap && r.message != nil {
		r.viewport.SetContent(r.render(r.message))
	}
}

func (r *reader) show(msg *mail.Message) {
	if r.message == msg && r.status == "" {
		return
	}
	r.message = msg
	r.status = ""
	r.viewport.SetContent(r.render(msg))
	r.viewport.SetYOffset(0)
}

func (r *reader) showPlaceholder(msg *mail.Message) {
	if msg == nil {
		return
	}
	r.message = msg
	r.status = "loading body…"
	r.viewport.SetContent(r.render(msg))
	r.viewport.SetYOffset(0)
}

func (r *reader) showError(err error) {
	r.status = sanitizeLine("could not load body: " + err.Error())
	if r.message != nil {
		r.viewport.SetContent(r.render(r.message))
	}
}

func (r *reader) render(msg *mail.Message) string {
	s := r.styles
	width := max(r.width, 20)
	var b strings.Builder
	line := func(label, value string) {
		if value != "" {
			b.WriteString(s.muted.Render(pad(label, 9)) + truncate(sanitizeLine(value), width-10) + "\n")
		}
	}
	b.WriteString(s.title.Render(truncate(sanitizeLine(msg.Subject), width)) + "\n")
	line("From", msg.Sender)
	line("To", strings.Join(msg.ToRecipients, ", "))
	line("Cc", strings.Join(msg.CcRecipients, ", "))
	line("Date", formatLongDate(msg.DateReceived))
	line("In", msg.Account+" / "+msg.Mailbox+"  id "+msg.ID)
	b.WriteString(s.chrome.Render(strings.Repeat("─", width)) + "\n")
	if r.status != "" {
		b.WriteString(s.muted.Render(r.status) + "\n")
	}
	switch {
	case msg.Content != "":
		for _, l := range strings.Split(sanitizeBody(msg.Content), "\n") {
			wrapped := ansi.Wrap(l, width, "")
			if strings.HasPrefix(l, ">") {
				wrapped = s.muted.Render(wrapped)
			}
			b.WriteString(wrapped + "\n")
		}
	case r.status == "":
		b.WriteString(s.muted.Render("(no text content)") + "\n")
	}
	return b.String()
}

func (r *reader) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	// Any key other than n cancels a step armed at the page boundary.
	m.advanceAfterPage = false
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
		before := m.list.cursor
		m.list.move(1)
		if m.list.cursor == before && m.list.hasMore {
			// At the loaded boundary: step onto the next row once it arrives.
			m.advanceAfterPage = true
			return m.loadMore()
		}
		return tea.Batch(m.requestBody(), m.loadMore())
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
		return block([]string{m.styles.muted.Render("select a message")}, r.width, r.height)
	}
	return block(strings.Split(r.viewport.View(), "\n"), r.width, r.height)
}
