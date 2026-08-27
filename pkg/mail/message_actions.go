package mail

import (
	"errors"
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
	if (mbox === null) {
		'Error: Mailbox not found';
	} else {
		const msg = messageById(mbox, '%s');
		if (msg === null) {
			'Error: Message not found';
		} else {
			%s
			'Success';
		}
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
		return bridgeError(output)
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
	if err := c.runMessageAction(accountName, mailboxName, messageID, "msg.delete();"); err != nil {
		return err
	}
	if err := RemoveRecentMessage(accountName, messageID); err != nil {
		c.recentCleanupWarningOnce.Do(func() {
			Warn(fmt.Sprintf("message was deleted, but recent-message history could not be updated (%v). Run mail-app-cli recent clear to remove stale entries.", err))
		})
	}
	return nil
}

func (c *Client) DeleteMessageResolved(accountName, mailboxName, messageID string) error {
	return deleteMessageResolved(mailboxName, func(candidateMailbox string) error {
		return c.DeleteMessage(accountName, candidateMailbox, messageID)
	})
}

func deleteMessageResolved(mailboxName string, deleteFromMailbox func(string) error) error {
	err := deleteFromMailbox(mailboxName)
	if err == nil {
		return nil
	}
	if !isMessageNotFoundError(err) {
		return err
	}

	for _, fallbackMailbox := range deleteFallbackMailboxes(mailboxName) {
		if fallbackErr := deleteFromMailbox(fallbackMailbox); fallbackErr == nil {
			return nil
		}
	}

	return err
}

func deleteFallbackMailboxes(mailboxName string) []string {
	if isArchiveAlias(mailboxName) {
		return nil
	}
	return []string{"All Mail", "Archive"}
}

func isMessageNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		return strings.EqualFold(notFound.Kind, "message")
	}
	return strings.Contains(strings.ToLower(err.Error()), "message not found")
}

func (c *Client) ArchiveMessage(accountName, mailboxName, messageID string) error {
	_, err := c.ArchiveMessageWithDestination(accountName, mailboxName, messageID)
	return err
}

func (c *Client) ArchiveMessageWithDestination(accountName, mailboxName, messageID string) (string, error) {
	script := archiveMessageScript(accountName, mailboxName, messageID)

	output, err := c.runAppleScript(script)
	if err != nil {
		return "", err
	}
	if strings.Contains(output, "Error") {
		return "", bridgeError(output)
	}
	return strings.TrimSpace(output), nil
}

func archiveMessageScript(accountName, mailboxName, messageID string) string {
	return fmt.Sprintf(`
on findMailboxByName(mailboxList, targetName)
	repeat with candidate in mailboxList
		try
			if name of candidate is targetName then return candidate
			set childMailbox to my findMailboxByName(mailboxes of candidate, targetName)
			if childMailbox is not missing value then return childMailbox
		end try
	end repeat
	return missing value
end findMailboxByName

tell application "Mail"
	set targetAccount to account "%s"
	set sourceMailbox to my findMailboxByName(mailboxes of targetAccount, "%s")
	if sourceMailbox is missing value then error "Mailbox not found: %s"

	set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "All Mail")
	if archiveMailbox is missing value then set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "Archive")
	if archiveMailbox is missing value then error "Archive mailbox not found"

	if name of sourceMailbox is name of archiveMailbox then
		return name of archiveMailbox
	end if

	set targetId to "%s" as integer
	set targetMessage to first message of sourceMailbox whose id is targetId
	move targetMessage to archiveMailbox
	return name of archiveMailbox
end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(mailboxName), escapeAppleScriptString(mailboxName), escapeAppleScriptString(messageID))
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
		return bridgeError(output)
	}
	return nil
}
