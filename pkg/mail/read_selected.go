package mail

import (
	"context"
	"fmt"
	"io"
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
	result, _ := c.ReadSelectedMessagesWithOptions(refs, timeout, budget, SelectedReadOptions{})
	return result
}

// SelectedReadOptions enables durable body extraction and machine-readable
// progress. A checkpoint is read-only evidence, never a mutation selection.
type SelectedReadOptions struct {
	Checkpoint string
	Account    string // An explicit CLI account constraint, when supplied.
	// SelectionIDs includes requested IDs that could not yet be located. When
	// omitted, the selection is derived from refs. Cached successes remain bound
	// to their captured account even when a later locator changes its answer.
	SelectionIDs []string
	Events       io.Writer
}

func (c *Client) ReadSelectedMessagesWithOptions(refs []MessageRef, timeout, budget time.Duration, opts SelectedReadOptions) (SelectedReadResult, error) {
	ctx, cancel := context.WithTimeout(c.Context(), budget)
	defer cancel()
	client := c.WithContext(ctx)
	itemClient := client
	var session *jxaSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	hooks := selectedReadHooks{}
	if opts.Checkpoint != "" {
		hooks.Begin = func() func() {
			itemContext, cancelItem := context.WithTimeout(ctx, timeout)
			itemClient = c.WithContext(itemContext)
			return func() {
				if session != nil {
					session.Close()
					session = nil
				}
				cancelItem()
			}
		}
		hooks.AccountID = func(account string) (string, error) { return itemClient.checkpointAccountID(account) }
		hooks.Recover = func(origin readCheckpointOrigin) (*Message, error) {
			return itemClient.recoverCheckpointMessage(origin)
		}
		hooks.Revision = func(ref MessageRef, identity *StableIdentity) (string, error) {
			return itemClient.checkpointContentRevision(ref, identity)
		}
		hooks.Confirm = func(ref MessageRef) (*Message, error) { return itemClient.checkpointExactMetadata(ref) }
	}
	return readSelectedWithCheckpoint(refs, opts, c.shared.bodyFromMail, c.shared.bodyAlternatives, func(ref MessageRef) (*Message, error) {
		return itemClient.GetMessageDetailsForVerificationJSON(ref.AccountName, ref.MailboxName, ref.MessageID)
	}, func(ref MessageRef, i int) (*Message, *PhaseTrace, error) {
		if err := itemClient.Done(); err != nil {
			return nil, nil, err
		}
		effectiveTimeout := checkpointRemainingTimeout(itemClient.Context(), timeout)
		if itemClient.nativeEnabled() {
			return itemClient.getMessageDetailsTraced(ref.AccountName, ref.MailboxName, ref.MessageID, effectiveTimeout, !c.shared.bodyFromMail)
		}
		if i%10 == 0 || (session != nil && session.closed) {
			if session != nil {
				session.Close()
				session = nil
			}
		}
		if session == nil {
			var err error
			session, err = newJXASession(itemClient.Context())
			if err != nil {
				return nil, nil, err
			}
			itemClient.session = session
		}
		return itemClient.getMessageDetailsTraced(ref.AccountName, ref.MailboxName, ref.MessageID, effectiveTimeout, !c.shared.bodyFromMail)
	}, hooks)
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
