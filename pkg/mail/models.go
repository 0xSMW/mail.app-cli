package mail

type Account struct {
	ID           string
	Name         string
	EmailAddress string
	AccountType  string
	UserName     string
	Enabled      bool
}

type Mailbox struct {
	Name        string
	UnreadCount int
	TotalCount  int
	Account     string
}

type Message struct {
	ID            string
	Subject       string
	Sender        string
	DateSent      string
	DateReceived  string
	Read          bool
	Flagged       bool
	Deleted       bool
	MessageSize   int
	Content       string
	Mailbox       string
	Account       string
	ToRecipients  []string
	CcRecipients  []string
	BccRecipients []string
}

type Attachment struct {
	Index    int
	Name     string
	FileSize int
	MimeType string
}
