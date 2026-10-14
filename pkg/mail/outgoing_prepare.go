package mail

import (
	"fmt"
	"strings"
)

// Ordinary and threaded composition share recipient and local-file policy.
// Account/action checks remain at their entrypoints to preserve error precedence.
func prepareOutgoing(input OutgoingInput) (OutgoingInput, error) {
	if len(input.To)+len(input.Cc)+len(input.Bcc) == 0 {
		return input, fmt.Errorf("at least one recipient is required")
	}
	for _, list := range [][]string{input.To, input.Cc, input.Bcc} {
		for _, address := range list {
			if strings.TrimSpace(address) == "" {
				return input, fmt.Errorf("recipient must not be empty")
			}
		}
	}
	var err error
	input.Attachments, err = ValidateDraftAttachments(input.Attachments)
	return input, err
}

func draftOutgoing(draft *Message, sender, body string) OutgoingInput {
	return OutgoingInput{Account: draft.Account, Sender: sender, Subject: draft.Subject, Body: body, To: draft.ToRecipients, Cc: draft.CcRecipients, Bcc: draft.BccRecipients}
}

func draftReplacement(account string, details *Message, sender, body string, input DraftInput) DraftInput {
	replacement := DraftInput{Account: account, Sender: sender, Subject: details.Subject, Body: body, To: details.ToRecipients, Cc: details.CcRecipients, Bcc: details.BccRecipients}
	if input.SubjectSet {
		replacement.Subject = input.Subject
	}
	if input.BodySet {
		replacement.Body = input.Body
	}
	return replacement
}
