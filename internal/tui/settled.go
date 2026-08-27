package tui

import (
	"time"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// settled is the state a receipt confirmed for a message, held until the
// index reports it too. The Envelope Index can lag Mail.app by seconds, so
// a refresh that still carries the old value would otherwise revert the
// screen and, for an unread row under an open reader, re-arm the automatic
// mark-read. Entries expire after settledTTL so a message the index never
// agrees about cannot pin the screen.
type settled struct {
	gone    bool
	read    *bool
	flagged *bool
	expires time.Time
}

const settledTTL = 30 * time.Second

// recordSettled remembers what a receipt confirmed for each succeeded item.
func (m *model) recordSettled(msg mutationDoneMsg, now time.Time) {
	for _, item := range msg.result.Items {
		if item.Status != "succeeded" {
			continue
		}
		key := item.Account + "\x00" + item.SourceMailbox + "\x00" + item.ID
		s := m.settled[key]
		switch msg.opts.Action {
		case "mark":
			v := msg.opts.Read
			s.read = &v
		case "flag":
			v := msg.opts.Flagged
			s.flagged = &v
		default:
			if !msg.removed[key] {
				continue
			}
			s.gone = true
		}
		s.expires = now.Add(settledTTL)
		if m.settled == nil {
			m.settled = map[string]settled{}
		}
		m.settled[key] = s
	}
}

// reconcile lays settled state over rows the index returned: rows a
// receipt removed are dropped, and read and flag values the receipt
// confirmed win. Entries the index now agrees with are forgotten. It
// reports whether the index is still behind, so the caller can look again.
func (m *model) reconcile(messages []mail.Message, now time.Time) ([]mail.Message, bool) {
	for key, s := range m.settled {
		if now.After(s.expires) {
			delete(m.settled, key)
		}
	}
	if len(m.settled) == 0 {
		return messages, false
	}
	out := make([]mail.Message, 0, len(messages))
	lagging := false
	for _, msg := range messages {
		key := bodyKey(msg)
		s, ok := m.settled[key]
		if !ok {
			out = append(out, msg)
			continue
		}
		if s.gone {
			lagging = true
			continue
		}
		agrees := true
		if s.read != nil && msg.Read != *s.read {
			msg.Read = *s.read
			agrees = false
		}
		if s.flagged != nil && msg.Flagged != *s.flagged {
			msg.Flagged = *s.flagged
			agrees = false
		}
		if agrees {
			delete(m.settled, key)
		} else {
			lagging = true
		}
		out = append(out, msg)
	}
	return out, lagging
}
