package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// requestResult is embedded in every read response so the lane that asked
// can tell a current answer from one the user has since moved past.
type requestResult struct {
	requestID uint64
	err       error
}

// requestLane tracks the one read a pane is waiting on. Beginning another
// supersedes it: the older read is cancelled and its late answer discarded.
type requestLane struct {
	id      uint64
	cancel  context.CancelFunc
	loading bool
}

// begin supersedes the read in flight. silent reads (refreshes after a
// mutation) do not show the spinner.
func (l *requestLane) begin(parent context.Context, silent bool) (uint64, context.Context) {
	if l.cancel != nil {
		l.cancel()
	}
	l.id++
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.loading = !silent
	return l.id, ctx
}

func (l *requestLane) accepts(result requestResult) bool {
	return result.requestID == l.id
}

// settle closes the read a response answers. It returns false when the
// response is stale or carries an error, and the error command when it does.
func (l *requestLane) settle(result requestResult) (tea.Cmd, bool) {
	if !l.accepts(result) {
		return nil, false
	}
	l.finish()
	if result.err != nil {
		err := result.err
		return func() tea.Msg { return errMsg{err} }, false
	}
	return nil, true
}

func (l *requestLane) finish() {
	if l.cancel != nil {
		l.cancel()
	}
	l.cancel = nil
	l.loading = false
}

func (l *requestLane) abandon() {
	l.finish()
	l.id++
}
