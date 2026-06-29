package mail

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (c *Client) GetAttachmentsJSON(accountName, mailboxName, messageID string) ([]Attachment, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const requestedMailbox = '%s';
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const allIds = mbox.messages.id();
	const targetIdx = allIds.findIndex(id => String(id) === '%s');
	if (targetIdx >= 0) {
		const attachments = mbox.messages.at(targetIdx).mailAttachments();
		for (let a = 0; a < attachments.length; a++) {
			const att = attachments[a];
			let mimeType = 'unknown';
			try {
				mimeType = att.mimeType() || 'unknown';
			} catch (e) {
				// mimeType() sometimes fails in Mail.app
			}
				result.push({
					index: a,
					name: att.name(),
					fileSize: att.fileSize(),
					mimeType: mimeType
			});
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var attachments []Attachment
	if err := json.Unmarshal([]byte(output), &attachments); err != nil {
		return nil, fmt.Errorf("failed to parse attachments JSON: %w", err)
	}

	return attachments, nil
}

func (c *Client) SaveAttachment(accountName, mailboxName, messageID, attachmentName, savePath string) error {
	return c.SaveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName, -1, savePath)
}

func (c *Client) SaveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName string, index int, savePath string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const app = Application.currentApplication();
app.includeStandardAdditions = true;
const requestedMailbox = '%s';
const requestedIndex = %d;
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		const attachments = msg.mailAttachments();
		let found = false;
		const pathObj = Path('%s');
		if (requestedIndex >= 0) {
			if (requestedIndex < attachments.length) {
				attachments[requestedIndex].save({ in: pathObj });
				found = true;
			}
		} else {
			for (let a = 0; a < attachments.length; a++) {
				if (attachments[a].name() === '%s') {
					attachments[a].save({ in: pathObj });
					found = true;
					break;
				}
			}
		}
		if (found) {
			'Success';
		} else {
			'Error: Attachment not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), index, jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), escapeJSString(savePath), escapeJSString(attachmentName))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}
