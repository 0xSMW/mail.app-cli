package mail

import "context"

// ListOptions selects messages from one mailbox.
type ListOptions struct {
	Account     string
	Mailbox     string
	Limit       int
	Offset      int
	UnreadOnly  bool
	FlaggedOnly bool
	WithContent bool
	Since       string
}

// ListMessages lists one mailbox, newest first, from the Envelope Index when
// it is readable and from Mail.app otherwise. Cancelling ctx stops any
// automation subprocess the read is waiting on.
func (c *Client) ListMessages(ctx context.Context, opts ListOptions) ([]Message, error) {
	return c.WithContext(ctx).GetMessagesJSON(opts.Account, opts.Mailbox, opts.Limit, opts.Offset, opts.UnreadOnly, opts.FlaggedOnly, opts.WithContent, opts.Since)
}

// ListUnified lists an inbox-style view (inbox, unread, flagged, sent, drafts,
// trash, junk) merged across every enabled account.
func (c *Client) ListUnified(ctx context.Context, kind string, limit, offset int, withContent bool) ([]Message, error) {
	return c.WithContext(ctx).GetUnifiedMessagesJSON(kind, limit, offset, withContent)
}

// MessageDetails fetches one message with its body through Mail.app.
func (c *Client) MessageDetails(ctx context.Context, account, mailbox, id string) (*Message, error) {
	return c.WithContext(ctx).GetMessageDetailsJSON(account, mailbox, id)
}

// Mailboxes lists mailboxes for one account, or every account when account is empty.
func (c *Client) Mailboxes(ctx context.Context, account string) ([]Mailbox, error) {
	return c.WithContext(ctx).GetMailboxesJSON(account)
}

// Accounts lists Mail.app accounts.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return c.WithContext(ctx).GetAccountsJSON()
}

// Search runs the standard search within an account and optional mailbox.
// Partial results are returned rather than refused; check Complete.
func (c *Client) Search(ctx context.Context, query, account, mailbox string, limit int) (SearchResult, error) {
	return c.WithContext(ctx).SearchMessagesJSONSinceWithOptions(query, account, mailbox, limit, "", SearchOptions{AllowPartial: true})
}
