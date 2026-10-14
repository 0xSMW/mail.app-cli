package mail

import (
	"fmt"
)

const maxConcurrentMailCommands = 4

func runWithMailCommandLimit[T any](items []T, run func(T)) {
	limit := min(len(items), maxConcurrentMailCommands)
	if limit <= 0 {
		return
	}
	sem := make(chan struct{}, limit)
	for _, item := range items {
		sem <- struct{}{}
		go func(v T) {
			defer func() { <-sem }()
			run(v)
		}(item)
	}
}

// Successful values and errors arrive in completion order. Ordered reads carry
// their request index as part of the value, preserving their original slots.
func collectMailCommands[R, V any](requests []R, run func(R) (V, error), accept func(V)) []error {
	type result struct {
		value V
		err   error
	}
	results := make(chan result, len(requests))
	runWithMailCommandLimit(requests, func(request R) {
		value, err := run(request)
		results <- result{value, err}
	})

	var errors []error
	for range requests {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			accept(res.value)
		}
	}
	return errors
}

// MailboxListRequest selects messages from one mailbox for a fan-out read.
type MailboxListRequest struct {
	AccountName string
	MailboxName string
	Limit       int
	Offset      int
	UnreadOnly  bool
	FlaggedOnly bool
	WithContent bool
	Since       string
}

// MessageRef names one message by account, mailbox, and ID.
type MessageRef struct {
	AccountName string
	MailboxName string
	MessageID   string
}

func (c *Client) GetMessagesFromMultipleMailboxes(requests []MailboxListRequest) ([]Message, error) {
	if len(requests) == 0 {
		return []Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		return c.GetMessagesJSON(req.AccountName, req.MailboxName, req.Limit, req.Offset, req.UnreadOnly, req.FlaggedOnly, req.WithContent, req.Since)
	}

	var allMessages []Message
	errors := collectMailCommands(requests, c.ListMessages, func(messages []Message) {
		allMessages = append(allMessages, messages...)
	})

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to get messages from all mailboxes: %v", errors)
	}

	return allMessages, nil
}

func (c *Client) GetMultipleMessageDetails(requests []MessageRef) ([]*Message, error) {
	if len(requests) == 0 {
		return []*Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		msg, err := c.GetMessageDetailsJSON(req.AccountName, req.MailboxName, req.MessageID)
		if err != nil {
			return nil, err
		}
		return []*Message{msg}, nil
	}

	type indexedRequest struct {
		index int
		req   MessageRef
	}
	type indexedMessage struct {
		index   int
		message *Message
	}
	indexed := make([]indexedRequest, 0, len(requests))
	for i, req := range requests {
		indexed = append(indexed, indexedRequest{i, req})
	}
	messages := make([]*Message, len(requests))
	errors := collectMailCommands(indexed, func(item indexedRequest) (indexedMessage, error) {
		message, err := c.GetMessageDetailsJSON(item.req.AccountName, item.req.MailboxName, item.req.MessageID)
		return indexedMessage{item.index, message}, err
	}, func(item indexedMessage) {
		messages[item.index] = item.message
	})

	// Return error if all requests failed
	if len(errors) == len(requests) {
		return nil, fmt.Errorf("failed to get all message details: %v", errors)
	}

	return messages, nil
}

func (c *Client) BulkMarkMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
	Read        bool
}) error {
	return runBulkOperations(requests, "failed to mark some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
		Read        bool
	}) error {
		return c.MarkMessageAsRead(req.AccountName, req.MailboxName, req.MessageID, req.Read)
	})
}

func (c *Client) BulkFlagMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
	Flagged     bool
}) error {
	return runBulkOperations(requests, "failed to flag some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
		Flagged     bool
	}) error {
		return c.FlagMessage(req.AccountName, req.MailboxName, req.MessageID, req.Flagged)
	})
}

func (c *Client) BulkDeleteMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) error {
	return runBulkOperations(requests, "failed to delete some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
	}) error {
		return c.DeleteMessage(req.AccountName, req.MailboxName, req.MessageID)
	})
}

func (c *Client) BulkArchiveMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) error {
	return runBulkOperations(requests, "failed to archive some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
	}) error {
		return c.ArchiveMessage(req.AccountName, req.MailboxName, req.MessageID)
	})
}

func runBulkOperations[T any](requests []T, failureMessage string, run func(T) error) error {
	if len(requests) == 0 {
		return nil
	}

	if len(requests) == 1 {
		return run(requests[0])
	}

	errorList := collectMailCommands(requests, func(req T) (struct{}, error) {
		return struct{}{}, run(req)
	}, func(struct{}) {})

	if len(errorList) > 0 {
		return fmt.Errorf("%s: %v", failureMessage, errorList)
	}

	return nil
}

func (c *Client) BulkMoveMessages(requests []struct {
	AccountName   string
	SourceMailbox string
	MessageID     string
	TargetMailbox string
}) error {
	return runBulkOperations(requests, "failed to move some messages", func(req struct {
		AccountName   string
		SourceMailbox string
		MessageID     string
		TargetMailbox string
	}) error {
		return c.MoveMessage(req.AccountName, req.SourceMailbox, req.MessageID, req.TargetMailbox)
	})
}
