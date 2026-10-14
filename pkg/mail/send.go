package mail

// SendMessage preserves the public API and succeeds only when Mail explicitly
// accepts the verified outgoing message for submission.
func (c *Client) SendMessage(accountName, subject, body string, to, cc, bcc, attachments []string) error {
	_, err := c.SendOutgoing(OutgoingInput{Account: accountName, Subject: subject, Body: body, To: to, Cc: cc, Bcc: bcc, Attachments: attachments})
	return err
}
