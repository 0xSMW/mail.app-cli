package mail

// IsGmailArchiveAccount checks Mail's actual incoming server host. A mailbox
// named All Mail or a Gmail-looking account name alone is insufficient.
func (c *Client) IsGmailArchiveAccount(account string) (bool, error) {
	return c.archiveUsesNative(account, "INBOX", "All Mail")
}
