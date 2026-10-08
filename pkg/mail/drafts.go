package mail

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type DraftInput struct {
	Account     string   `json:"account"`
	Sender      string   `json:"sender,omitempty"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	SubjectSet  bool     `json:"-"`
	BodySet     bool     `json:"-"`
}

func (c *Client) CreateDraft(input DraftInput) (*Message, error) {
	result, err := c.composeOutgoing(OutgoingInput{Account: input.Account, Sender: input.Sender, Subject: input.Subject, Body: input.Body, To: input.To, Cc: input.Cc, Bcc: input.Bcc, Attachments: input.Attachments}, "save")
	if err != nil {
		if result != nil && result.Draft != nil {
			return result.Draft, err
		}
		return nil, err
	}
	if result == nil || result.Draft == nil {
		return nil, fmt.Errorf("draft save returned no verified draft")
	}

	return result.Draft, nil
}

func (c *Client) UpdateDraft(accountName, draftID string, input DraftInput) (*Message, error) {
	attachments, err := ValidateDraftAttachments(input.Attachments)
	if err != nil {
		return nil, err
	}
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return nil, err
	}
	details, err := c.getMessageDetailsFromMail(draft.Account, draft.Mailbox, draft.ID)
	if err != nil {
		return nil, err
	}
	if details == nil {
		return nil, notFound("draft", draftID)
	}
	if details.ContentError != "" {
		return nil, fmt.Errorf("cannot preserve draft body: %s", details.ContentError)
	}
	sender, err := draftSenderAddress(details.Sender)
	if err != nil {
		return nil, err
	}
	replacement := DraftInput{
		Account: draft.Account,
		Sender:  sender,
		Subject: details.Subject,
		Body:    details.Content,
		To:      details.ToRecipients,
		Cc:      details.CcRecipients,
		Bcc:     details.BccRecipients,
	}
	if input.SubjectSet {
		replacement.Subject = input.Subject
	}
	if input.BodySet {
		replacement.Body = input.Body
	}
	if replacement.Subject == details.Subject && replacement.Body == details.Content && len(attachments) == 0 {
		return draft, nil
	}
	if len(replacement.To) == 0 {
		return nil, fmt.Errorf("draft has no to recipients to preserve")
	}
	// Saved drafts are replaced, so export every attachment before creating the
	// replacement. Fail closed on enumeration/save errors; keep the original.
	dir, err := os.MkdirTemp("", "mail-app-cli-draft-attachments-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	preserved, err := c.exportDraftAttachments(draft, dir)
	if err != nil {
		return nil, fmt.Errorf("preserve draft attachments: %w", err)
	}
	replacement.Attachments = append(preserved, attachments...)
	updated, err := c.CreateDraft(replacement)
	if err != nil {
		return nil, err
	}
	if err := c.deleteDraftByID(draft.Account, draft.Mailbox, draft.ID); err != nil {
		return nil, fmt.Errorf("created updated draft %s but failed to delete original draft %s: %w", updated.ID, draft.ID, err)
	}
	return updated, nil
}

func (c *Client) GetDraft(accountName, draftID string) (*Message, error) {
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		messages, err := c.GetUnifiedMessagesJSON("drafts", pageSize, offset, true)
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			if message.ID == draftID && (accountName == "" || message.Account == accountName) {
				return &message, nil
			}
		}
		if len(messages) < pageSize {
			break
		}
	}
	return nil, notFound("draft", draftID)
}

func (c *Client) SendDraft(accountName, draftID string) error {
	_, err := c.SendDraftResult(accountName, draftID)
	return err
}

func (c *Client) DeleteDraft(accountName, draftID string) error {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return err
	}
	if err := c.deleteDraftByID(draft.Account, draft.Mailbox, draft.ID); err != nil {
		return err
	}
	return nil
}

func (c *Client) findDraftBySubject(accountName, subject string) (*Message, error) {
	return c.findDraftBySubjectContentSince(accountName, subject, "", time.Time{})
}

func (c *Client) findDraftBySubjectContentSince(accountName, subject, content string, since time.Time) (*Message, error) {
	var best *Message
	const pageSize = 500
	for offset := 0; ; offset += pageSize {
		messages, err := c.GetUnifiedMessagesJSON("drafts", pageSize, offset, true)
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			if message.Subject != subject || (accountName != "" && message.Account != accountName) {
				continue
			}
			if content != "" && normalizeDraftContent(message.Content) != normalizeDraftContent(content) {
				continue
			}
			if !since.IsZero() {
				messageTime, ok := parseMessageTimestamp(message.DateReceived)
				if !ok || messageTime.Before(since) {
					continue
				}
			}
			if best == nil || message.DateReceived > best.DateReceived {
				copy := message
				best = &copy
			}
		}
		if len(messages) < pageSize {
			break
		}
	}
	if best == nil {
		return nil, fmt.Errorf("draft not found by subject: %s", subject)
	}
	return best, nil
}

// ParseMessageTime parses the timestamp shapes Mail.app and the Envelope
// Index produce.
func ParseMessageTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseMessageTimestamp(value string) (time.Time, bool) { return ParseMessageTime(value) }

func normalizeDraftContent(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func (c *Client) deleteDraftByID(accountName, mailboxName, draftID string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg === null) {
		'Error: Draft not found';
	} else {
		msg.delete();
		'Success';
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(draftID))
	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return bridgeError(output)
	}
	return nil
}
