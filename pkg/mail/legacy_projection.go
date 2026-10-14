package mail

import "fmt"

// jxaMessageProjection holds expressions evaluated in field order. Keeping
// these inline preserves each caller's Mail getter counts and catch policy.
type jxaMessageProjection struct {
	ID, RFCMessageID, Size, Content, Mailbox, Account string
}

func (p jxaMessageProjection) fields() string {
	rfc := ""
	if p.RFCMessageID != "" {
		rfc = "rfcMessageId: " + p.RFCMessageID + ","
	}
	return fmt.Sprintf(`
	id: %s,
	%s
	subject: msg.subject() || '',
	sender: msg.sender() || '',
	dateReceived: (msg.dateReceived() || new Date()).toISOString(),
	dateSent: (msg.dateSent() || new Date()).toISOString(),
	read: msg.readStatus(),
	flagged: msg.flaggedStatus(),
	messageSize: %s,
	%s
	mailbox: %s,
	account: %s
`, p.ID, rfc, p.Size, p.Content, p.Mailbox, p.Account)
}
