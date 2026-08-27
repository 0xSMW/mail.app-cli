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

func notifyProblem(text string) tea.Cmd {
	return func() tea.Msg { return notifyMsg{text: text, kind: toastError} }
}

func notifyError(what string, err error) tea.Cmd {
	return notifyProblem(what + ": " + err.Error())
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
	frame, text := m.styles.frame, m.styles.positive
	if m.toast.kind == toastError {
		frame, text = m.styles.errorFrame, m.styles.error
	}
	return frame.Render(text.Render(truncate(sanitizeLine(m.toast.text), max(m.width/2, 20))))
}
