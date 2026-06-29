package mail

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"
)

type messageDateMinHeap []Message

func (h messageDateMinHeap) Len() int { return len(h) }

func (h messageDateMinHeap) Less(i, j int) bool {
	return h[i].DateReceived < h[j].DateReceived
}

func (h messageDateMinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *messageDateMinHeap) Push(x any) {
	*h = append(*h, x.(Message))
}

func (h *messageDateMinHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func (c *Client) GetUnifiedMessagesJSON(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	switch mailboxType {
	case "inbox", "unread", "flagged":
		return c.getInboxBasedUnified(mailboxType, limit, offset, withContent)
	case "sent", "drafts", "trash", "junk":
		return c.getSpecialMailboxUnified(mailboxType, limit, offset, withContent)
	default:
		return nil, fmt.Errorf("unknown unified mailbox type: %s", mailboxType)
	}
}

func (c *Client) getInboxBasedUnified(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	// Over-fetch per account so the global sort+slice is accurate.
	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	type req = struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}

	var requests []req
	for _, acc := range accounts {
		if !acc.Enabled {
			continue
		}
		requests = append(requests, req{
			AccountName: acc.Name,
			MailboxName: "INBOX",
			Limit:       perLimit,
			Offset:      0,
			UnreadOnly:  mailboxType == "unread",
			FlaggedOnly: mailboxType == "flagged",
			WithContent: withContent,
		})
	}

	if len(requests) == 0 {
		return []Message{}, nil
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}

	return sortAndSlice(messages, offset, limit), nil
}

func (c *Client) getSpecialMailboxUnified(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	allMailboxes, err := c.GetMailboxesJSON("")
	if err != nil {
		return nil, err
	}

	type req = struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}

	seen := make(map[string]bool)
	var requests []req
	for _, mailbox := range allMailboxes {
		if mailbox.Account == "" || mailbox.Name == "" || !isSpecialMailboxName(mailboxType, mailbox.Name) {
			continue
		}
		key := mailbox.Account + "\x00" + mailbox.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		requests = append(requests, req{
			AccountName: mailbox.Account,
			MailboxName: mailbox.Name,
			Limit:       perLimit,
			WithContent: withContent,
		})
	}

	if len(requests) == 0 {
		return []Message{}, nil
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}

	return sortAndSlice(messages, offset, limit), nil
}

func isSpecialMailboxName(mailboxType, mailboxName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(mailboxName))
	candidates := map[string][]string{
		"sent":   {"sent", "sent messages", "sent mail", "sent items"},
		"drafts": {"drafts", "draft"},
		"trash":  {"trash", "deleted messages", "deleted items", "bin"},
		"junk":   {"junk", "spam", "junk e-mail", "junk email", "category_spam"},
	}
	for _, candidate := range candidates[mailboxType] {
		if normalized == candidate {
			return true
		}
	}
	return false
}

func sortAndSlice(messages []Message, offset, limit int) []Message {
	if limit > 0 {
		keep := offset + limit
		if keep > 0 && keep < len(messages) {
			h := make(messageDateMinHeap, 0, keep)
			for _, message := range messages {
				if len(h) < keep {
					heap.Push(&h, message)
					continue
				}
				if message.DateReceived > h[0].DateReceived {
					h[0] = message
					heap.Fix(&h, 0)
				}
			}
			messages = h
		}
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].DateReceived > messages[j].DateReceived
	})
	if offset > 0 {
		if offset >= len(messages) {
			return []Message{}
		}
		messages = messages[offset:]
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[:limit]
	}
	return messages
}
