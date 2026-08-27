package mail

// These are the option-struct entry points a caller with a context-bound
// client uses; see WithContext. They forward to the positional methods the
// rest of the package still uses.

// ListMessages lists one mailbox, newest first, from the Envelope Index when
// it is readable and from Mail.app otherwise.
func (c *Client) ListMessages(req MailboxListRequest) ([]Message, error) {
	return c.GetMessagesJSON(req.AccountName, req.MailboxName, req.Limit, req.Offset, req.UnreadOnly, req.FlaggedOnly, req.WithContent, req.Since)
}

// ListUnified lists an inbox-style view (inbox, unread, flagged, sent, drafts,
// trash, junk) merged across every enabled account.
func (c *Client) ListUnified(kind string, limit int) ([]Message, error) {
	return c.GetUnifiedMessagesJSON(kind, limit, 0, false)
}

// MessageDetails fetches one message with its body through Mail.app.
func (c *Client) MessageDetails(account, mailbox, id string) (*Message, error) {
	return c.GetMessageDetailsJSON(account, mailbox, id)
}

// Mailboxes lists mailboxes for one account, or every account when account is empty.
func (c *Client) Mailboxes(account string) ([]Mailbox, error) {
	return c.GetMailboxesJSON(account)
}

// Accounts lists Mail.app accounts.
func (c *Client) Accounts() ([]Account, error) {
	return c.GetAccountsJSON()
}

// Search runs the standard search within an account (every account when
// empty). Partial results are returned rather than refused; check Complete.
func (c *Client) Search(query, account string, limit int) (SearchResult, error) {
	return c.SearchMessagesJSONSinceWithOptions(query, account, "", limit, "", SearchOptions{AllowPartial: true})
}
