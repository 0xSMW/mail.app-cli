package mail

import (
	"fmt"
)

const maxConcurrentMailCommands = 4

func runWithMailCommandLimit[T any](items []T, run func(T)) {
	limit := maxConcurrentMailCommands
	if len(items) < limit {
		limit = len(items)
	}
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

func (c *Client) GetMessagesFromMultipleMailboxes(requests []struct {
	AccountName string
	MailboxName string
	Limit       int
	Offset      int
	UnreadOnly  bool
	FlaggedOnly bool
	WithContent bool
	Since       string
}) ([]Message, error) {
	if len(requests) == 0 {
		return []Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		return c.GetMessagesJSON(req.AccountName, req.MailboxName, req.Limit, req.Offset, req.UnreadOnly, req.FlaggedOnly, req.WithContent, req.Since)
	}

	// Load messages from multiple mailboxes in parallel
	type result struct {
		messages []Message
		err      error
	}
	results := make(chan result, len(requests))

	// Launch bounded goroutines for mailbox retrieval.
	runWithMailCommandLimit(requests, func(r struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}) {
		messages, err := c.GetMessagesJSON(r.AccountName, r.MailboxName, r.Limit, r.Offset, r.UnreadOnly, r.FlaggedOnly, r.WithContent, r.Since)
		results <- result{messages: messages, err: err}
	})

	// Collect results
	var allMessages []Message
	var errors []error
	for i := 0; i < len(requests); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMessages = append(allMessages, res.messages...)
		}
	}

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to get messages from all mailboxes: %v", errors)
	}

	return allMessages, nil
}

func (c *Client) GetMultipleMessageDetails(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) ([]*Message, error) {
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

	// Load message details in parallel
	type result struct {
		message *Message
		err     error
		index   int
	}
	results := make(chan result, len(requests))

	type indexedRequest struct {
		index int
		req   struct {
			AccountName string
			MailboxName string
			MessageID   string
		}
	}
	indexedRequests := make([]indexedRequest, 0, len(requests))
	for i, req := range requests {
		indexedRequests = append(indexedRequests, indexedRequest{index: i, req: req})
	}
	runWithMailCommandLimit(indexedRequests, func(item indexedRequest) {
		message, err := c.GetMessageDetailsJSON(item.req.AccountName, item.req.MailboxName, item.req.MessageID)
		results <- result{message: message, err: err, index: item.index}
	})

	// Collect results in original order
	messages := make([]*Message, len(requests))
	var errors []error
	successCount := 0

	for i := 0; i < len(requests); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
			messages[res.index] = nil
		} else {
			messages[res.index] = res.message
			successCount++
		}
	}

	// Return error if all requests failed
	if successCount == 0 {
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

	errors := make(chan error, len(requests))

	runWithMailCommandLimit(requests, func(r T) {
		errors <- run(r)
	})

	var errorList []error
	for i := 0; i < len(requests); i++ {
		if err := <-errors; err != nil {
			errorList = append(errorList, err)
		}
	}

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
