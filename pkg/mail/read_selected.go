package mail

import (
	"context"
	"fmt"
	"time"
)

type MessageReadResult struct {
	ID      string   `json:"id"`
	Account string   `json:"account"`
	Mailbox string   `json:"mailbox"`
	Message *Message `json:"message,omitempty"`
	Error   string   `json:"error,omitempty"`
	// Diagnostics times each phase of the read. After a timeout, Pending names
	// the operation Mail.app had not answered.
	Diagnostics *PhaseTrace `json:"diagnostics,omitempty"`
}

type SelectedReadResult struct {
	Items    []MessageReadResult `json:"items"`
	Complete bool                `json:"complete"`
}

// ReadSelectedMessages preserves every successful body even when another ID
// is missing or stalls. A timed-out read is never retried automatically. The
// next ID gets a fresh bridge; the overall budget bounds all reads and queueing.
func (c *Client) ReadSelectedMessages(refs []MessageRef, timeout, budget time.Duration) SelectedReadResult {
	ctx, cancel := context.WithTimeout(c.Context(), budget)
	defer cancel()
	client := c.WithContext(ctx)
	var session *jxaSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	return readSelectedMessages(refs, func(ref MessageRef, i int) (*Message, *PhaseTrace, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if i%10 == 0 || (session != nil && session.closed) {
			if session != nil {
				session.Close()
				session = nil
			}
		}
		if session == nil {
			var err error
			session, err = newJXASession(ctx)
			if err != nil {
				return nil, nil, err
			}
			client.session = session
		}
		return client.getMessageDetailsTraced(ref.AccountName, ref.MailboxName, ref.MessageID, timeout, !c.shared.bodyFromMail)
	})
}

func readSelectedMessages(refs []MessageRef, read func(MessageRef, int) (*Message, *PhaseTrace, error)) SelectedReadResult {
	result := SelectedReadResult{Items: []MessageReadResult{}, Complete: true}
	for i, ref := range refs {
		item := MessageReadResult{ID: ref.MessageID, Account: ref.AccountName, Mailbox: ref.MailboxName}
		message, trace, err := read(ref, i)
		item.Message = message
		item.Diagnostics = trace
		if err == nil && message == nil {
			err = fmt.Errorf("message not found")
		}
		if err == nil && message.ContentError != "" {
			err = fmt.Errorf("body unavailable: %s", message.ContentError)
		}
		if err != nil {
			item.Error = err.Error()
			result.Complete = false
		}
		result.Items = append(result.Items, item)
	}
	return result
}
