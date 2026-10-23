package mail

import "time"

// UnsubscribeEvidence identifies where a destination was observed. It never
// establishes that a destination was visited or that an action succeeded.
type UnsubscribeEvidence struct {
	MessageID string `json:"messageId"`
	Source    string `json:"source"`
	MIMEPart  string `json:"mimePart,omitempty"`
	Text      string `json:"text,omitempty"`
	Kind      string `json:"kind"`
	Selected  bool   `json:"selected"`
}

type UnsubscribeDestination struct {
	URL      string                `json:"url"`
	Kind     string                `json:"kind"`
	Openable bool                  `json:"openable"`
	Evidence []UnsubscribeEvidence `json:"evidence"`
}

// UnsubscribeObservation is a compact local discovery result, without the
// message body, suitable for a private resumable discovery checkpoint.
type UnsubscribeObservation struct {
	ID                  string                   `json:"id"`
	RFCMessageID        string                   `json:"rfcMessageId,omitempty"`
	Account             string                   `json:"account"`
	Mailboxes           []string                 `json:"mailboxes"`
	Sender              string                   `json:"sender"`
	Subject             string                   `json:"subject"`
	DateReceived        string                   `json:"dateReceived"`
	ListID              string                   `json:"listId,omitempty"`
	HeaderStatus        string                   `json:"headerStatus"`
	Complete            bool                     `json:"complete"`
	Error               string                   `json:"error,omitempty"`
	Warnings            []string                 `json:"warnings,omitempty"`
	OneClickAdvertised  bool                     `json:"oneClickAdvertised"`
	HasHTTPSUnsubscribe bool                     `json:"hasHttpsUnsubscribe"`
	Destinations        []UnsubscribeDestination `json:"destinations"`
}

type UnsubscribeGroup struct {
	Key              string                   `json:"key"`
	KeySource        string                   `json:"keySource"`
	Provisional      bool                     `json:"provisional"`
	Account          string                   `json:"account"`
	ListID           string                   `json:"listId,omitempty"`
	Senders          []string                 `json:"senders"`
	MessageCount     int                      `json:"messageCount"`
	Latest           UnsubscribeObservation   `json:"latest"`
	Messages         []UnsubscribeObservation `json:"messages"`
	Destinations     []UnsubscribeDestination `json:"destinations"`
	DestinationsVary bool                     `json:"destinationsVary"`
}

type UnsubscribeReadability struct {
	Enumerated        int `json:"enumerated"`
	Inspected         int `json:"inspected"`
	Complete          int `json:"complete"`
	Partial           int `json:"partial"`
	Unavailable       int `json:"unavailable"`
	WithOpportunities int `json:"withOpportunities"`
}

type UnsubscribeScanRequest struct {
	Scopes          []SearchMailbox
	Since           string
	PageSize        int
	MaxPages        int
	Budget          time.Duration
	Checkpoint      string
	Restart         bool
	RetryIncomplete bool
}

type UnsubscribeScanResult struct {
	Groups            []UnsubscribeGroup       `json:"groups"`
	Coverage          []ScanCoverage           `json:"coverage"`
	Readability       UnsubscribeReadability   `json:"readability"`
	Exceptions        []UnsubscribeObservation `json:"exceptions"`
	TraversalComplete bool                     `json:"traversalComplete"`
	Complete          bool                     `json:"complete"`
	Scope             string                   `json:"scope"`
	Checkpoint        string                   `json:"checkpoint"`
	Resumed           bool                     `json:"resumed"`
	Pages             int                      `json:"pages"`
	Pending           int                      `json:"pending"`
	StartedAt         string                   `json:"startedAt"`
	EndedAt           string                   `json:"endedAt"`
}
