package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// A toast says what just happened in the top-right corner and takes itself
// away. Errors stay a little longer.
const (
	toastDuration      = 2500 * time.Millisecond
	toastErrorDuration = 5 * time.Second
)

type toastKind int

const (
	toastInfo toastKind = iota
	toastError
)

type notifyMsg struct {
	text string
	kind toastKind
}

type toastExpiredMsg struct{ id uint64 }

func notify(text string) tea.Cmd {
	return func() tea.Msg { return notifyMsg{text: text} }
}

func notifyError(what string, err error) tea.Cmd {
	return func() tea.Msg { return notifyMsg{text: what + ": " + err.Error(), kind: toastError} }
}

func (m *model) showToast(msg notifyMsg) tea.Cmd {
	m.toastID++
	m.toast = msg
	id := m.toastID
	duration := toastDuration
	if msg.kind == toastError {
		duration = toastErrorDuration
	}
	return tea.Tick(duration, func(time.Time) tea.Msg { return toastExpiredMsg{id: id} })
}

func (m model) toastView() string {
	if m.toast.text == "" {
		return ""
	}
	style := m.styles.frame
	text := m.styles.positive
	if m.toast.kind == toastError {
		style = style.BorderForeground(m.styles.palette.alert)
		text = m.styles.error
	}
	body := truncate(sanitizeLine(m.toast.text), max(m.width/2, 20))
	return style.Render(text.Render(body))
}
