package mail

import "encoding/json"

// SchemaVersion identifies the JSON shape of every structured value this
// package emits. Bump it when a field is renamed or removed.
const SchemaVersion = 1

type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	EmailAddress string `json:"emailAddress"`
	AccountType  string `json:"accountType"`
	UserName     string `json:"userName"`
	Enabled      bool   `json:"enabled"`
}

type Mailbox struct {
	Name        string `json:"name"`
	UnreadCount int    `json:"unreadCount"`
	TotalCount  int    `json:"totalCount"`
	Account     string `json:"account"`
}

type Message struct {
	ID            string   `json:"id"`
	Subject       string   `json:"subject"`
	Sender        string   `json:"sender"`
	DateSent      string   `json:"dateSent"`
	DateReceived  string   `json:"dateReceived"`
	Read          bool     `json:"read"`
	Flagged       bool     `json:"flagged"`
	Deleted       bool     `json:"deleted"`
	MessageSize   int      `json:"messageSize"`
	Content       string   `json:"content"`
	Mailbox       string   `json:"mailbox"`
	Account       string   `json:"account"`
	ToRecipients  []string `json:"toRecipients"`
	CcRecipients  []string `json:"ccRecipients"`
	BccRecipients []string `json:"bccRecipients"`
}

// MarshalJSON keeps recipient lists as arrays so consumers can always
// iterate them; a nil slice would otherwise serialize as null.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	p := plain(m)
	if p.ToRecipients == nil {
		p.ToRecipients = []string{}
	}
	if p.CcRecipients == nil {
		p.CcRecipients = []string{}
	}
	if p.BccRecipients == nil {
		p.BccRecipients = []string{}
	}
	return json.Marshal(p)
}

type Attachment struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	FileSize int    `json:"fileSize"`
	MimeType string `json:"mimeType"`
}
