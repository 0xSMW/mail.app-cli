package mail

import (
	"fmt"

	"golang.org/x/text/unicode/norm"
)

// attachmentNamesEqual accepts canonical Unicode spelling variants while
// preserving case and compatibility distinctions in publisher filenames.
func attachmentNamesEqual(a, b string) bool {
	return norm.NFC.String(a) == norm.NFC.String(b)
}

func (c *Client) GetAttachmentsJSON(accountName, mailboxName, messageID string) ([]Attachment, error) {
	if c.nativeEnabled() {
		return c.nativeAttachments(accountName, mailboxName, messageID)
	}
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

	var attachments []Attachment
	if err := c.runJXAJSON(script, defaultAutomationTimeout, "attachments", &attachments); err != nil {
		return nil, err
	}

	return attachments, nil
}

func (c *Client) SaveAttachment(accountName, mailboxName, messageID, attachmentName, savePath string) error {
	return c.SaveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName, -1, savePath)
}

func (c *Client) SaveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName string, index int, savePath string) error {
	if c.nativeEnabled() {
		return c.nativeSaveAttachment(accountName, mailboxName, messageID, attachmentName, index, savePath)
	}
	return c.saveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName, index, savePath, jxaMailboxLookupHelper()+jxaMessageByIdHelper(), jxaMailboxLookupExpression(mailboxName))
}

// Callers preserving a draft supply its account-owned special mailbox lookup;
// ordinary attachment commands retain their explicit mailbox-name lookup.
func (c *Client) saveAttachmentByIndex(accountName, mailboxName, messageID, attachmentName string, index int, savePath, lookupHelper, lookupExpression string) error {
	if c.nativeEnabled() {
		return c.nativeSaveAttachment(accountName, mailboxName, messageID, attachmentName, index, savePath)
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const app = Application.currentApplication();
app.includeStandardAdditions = true;
const requestedMailbox = '%s';
const requestedIndex = %d;
const requestedName = '%s';
function canonicalName(name) { return String(name).normalize('NFC'); }
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		const attachments = msg.mailAttachments();
		let selected = -1;
		const pathObj = Path('%s');
		if (requestedIndex >= 0) {
			if (requestedIndex < attachments.length) {
				if (requestedName !== '' && canonicalName(attachments[requestedIndex].name()) !== canonicalName(requestedName)) {
					throw new Error('Attachment index has unexpected name');
				}
				selected = requestedIndex;
			}
		} else {
			for (let a = 0; a < attachments.length; a++) {
				if (canonicalName(attachments[a].name()) === canonicalName(requestedName)) {
					if (selected >= 0) {
						throw new Error('Attachment name is ambiguous; select its index');
					}
					selected = a;
				}
			}
		}
		if (selected >= 0) {
			attachments[selected].save({ in: pathObj });
			'Success';
		} else {
			'Error: Attachment not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), index, escapeJSString(attachmentName), lookupHelper, escapeJSString(accountName), lookupExpression, escapeJSString(messageID), escapeJSString(savePath))

	return c.runJXAStatus(script)
}
