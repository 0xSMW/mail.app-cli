package mail

import (
	"fmt"
	"strings"
	"time"
)

type DraftInput struct {
	Account     string   `json:"account"`
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
	startedAt := time.Now().Add(-1 * time.Second)
	if input.Account == "" {
		return nil, fmt.Errorf("account is required")
	}
	if len(input.To) == 0 {
		return nil, fmt.Errorf("at least one to recipient is required")
	}
	script := fmt.Sprintf(`
tell application "Mail"
	set targetAccount to account "%s"
	set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}
	tell newMessage
		set sender to (item 1 of (email addresses of targetAccount as list))
		repeat with addr in {%s}
			make new to recipient at end of to recipients with properties {address:addr}
		end repeat
		%s
		%s
	end tell
	close newMessage saving yes
	return "ok"
end tell
`, escapeAppleScriptString(input.Account), escapeAppleScriptString(input.Subject), escapeAppleScriptString(input.Body), appleScriptStringList(input.To), appleScriptRecipientBlock("cc", input.Cc), appleScriptRecipientBlock("bcc", input.Bcc))
	if _, err := c.runAppleScript(script); err != nil {
		return nil, err
	}
	time.Sleep(5 * time.Second)
	if draft, err := c.findDraftBySubjectContentSince(input.Account, input.Subject, input.Body, startedAt); err == nil {
		return draft, nil
	}
	return nil, fmt.Errorf("created draft but could not resolve saved draft metadata")
}

func (c *Client) UpdateDraft(accountName, draftID string, input DraftInput) (*Message, error) {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return nil, err
	}
	details, err := c.GetMessageDetailsJSON(draft.Account, draft.Mailbox, draft.ID)
	if err != nil {
		return nil, err
	}
	replacement := DraftInput{
		Account: draft.Account,
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
	if replacement.Subject == details.Subject && replacement.Body == details.Content {
		return draft, nil
	}
	if len(replacement.To) == 0 {
		return nil, fmt.Errorf("draft has no to recipients to preserve")
	}
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
	return nil, fmt.Errorf("draft not found: %s", draftID)
}

func (c *Client) SendDraft(accountName, draftID string) error {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return err
	}
	return c.runMessageAction(draft.Account, draft.Mailbox, draft.ID, "msg.send();")
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

func parseMessageTimestamp(value string) (time.Time, bool) {
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

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
		return fmt.Errorf(output)
	}
	return nil
}
