package mail

import (
	"fmt"
	"strings"
)

func (c *Client) MarkMessageAsRead(accountName, mailboxName, messageID string, read bool) error {
	return c.runMessageAction(
		accountName,
		mailboxName,
		messageID,
		fmt.Sprintf("msg.readStatus = %s;", jxaBool(read)),
	)
}

func (c *Client) runMessageAction(accountName, mailboxName, messageID, action string) error {
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
		'Error: Message not found';
	} else {
		%s
		'Success';
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), action)

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

func (c *Client) FlagMessage(accountName, mailboxName, messageID string, flagged bool) error {
	return c.runMessageAction(
		accountName,
		mailboxName,
		messageID,
		fmt.Sprintf("msg.flaggedStatus = %s;", jxaBool(flagged)),
	)
}

func (c *Client) DeleteMessage(accountName, mailboxName, messageID string) error {
	return c.runMessageAction(accountName, mailboxName, messageID, "msg.delete();")
}

func (c *Client) ArchiveMessage(accountName, mailboxName, messageID string) error {
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
		'Error: Message not found';
	} else {
		function findArchiveCandidates(mailboxes, candidates) {
			for (let j = 0; j < mailboxes.length; j++) {
				const name = mailboxes[j].name();
				if (name === 'All Mail' || name === 'Archive') {
					candidates.push({ name: name, mailbox: mailboxes[j] });
				}
				try {
					const sub = mailboxes[j].mailboxes();
					if (sub.length > 0) {
						findArchiveCandidates(sub, candidates);
					}
				} catch(e) {}
			}
		}

		const archiveCandidates = [];
		findArchiveCandidates(acc.mailboxes(), archiveCandidates);
		let archiveBox = null;
		for (let i = 0; i < archiveCandidates.length; i++) {
			if (archiveCandidates[i].name === 'All Mail') {
				archiveBox = archiveCandidates[i].mailbox;
				break;
			}
		}
		if (!archiveBox) {
			for (let i = 0; i < archiveCandidates.length; i++) {
				if (archiveCandidates[i].name === 'Archive') {
					archiveBox = archiveCandidates[i].mailbox;
					break;
				}
			}
		}
		if (archiveBox) {
			msg.mailbox = archiveBox;
			let sourceName = '';
			let archiveName = '';
			try { sourceName = mbox.name(); } catch(e) {}
			try { archiveName = archiveBox.name(); } catch(e) {}
			if (sourceName === archiveName) {
				'Success';
			} else {
				const remaining = messageById(mbox, '%s');
				if (remaining === null) {
					'Success';
				} else {
					'Error: Archive did not move message out of source mailbox';
				}
			}
		} else {
			'Error: Archive mailbox not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

func (c *Client) MoveMessage(accountName, sourceMailbox, messageID, targetMailbox string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
const requestedTargetMailbox = '%s';
%s
%s
try {
	const acc = mail.accounts.byName('%s');
	const sourceMbox = %s;
	const msg = messageById(sourceMbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		const destMbox = %s;
		msg.mailbox = destMbox;
		'Success';
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(sourceMailbox), escapeJSString(targetMailbox), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(sourceMailbox), escapeJSString(messageID), jxaMailboxLookupExpressionFor(targetMailbox, "requestedTargetMailbox"))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}
