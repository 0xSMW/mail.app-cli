package mail

import (
	"errors"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func (c *Client) MarkMessageAsRead(accountName, mailboxName, messageID string, read bool) error {
	if c.nativeEnabled() {
		return c.nativeMessageAction(accountName, mailboxName, messageID, "isrd", appleevents.Bool(read))
	}
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

	return c.runJXAStatus(script)
}

func (c *Client) FlagMessage(accountName, mailboxName, messageID string, flagged bool) error {
	if c.nativeEnabled() {
		return c.nativeMessageAction(accountName, mailboxName, messageID, "isfl", appleevents.Bool(flagged))
	}
	return c.runMessageAction(
		accountName,
		mailboxName,
		messageID,
		fmt.Sprintf("msg.flaggedStatus = %s;", jxaBool(flagged)),
	)
}

func (c *Client) DeleteMessage(accountName, mailboxName, messageID string) error {
	var actionErr error
	if c.nativeEnabled() {
		actionErr = c.nativeMessageAction(accountName, mailboxName, messageID, "delete", appleevents.Null())
	} else {
		actionErr = c.runMessageAction(accountName, mailboxName, messageID, "msg.delete();")
	}
	if err := actionErr; err != nil {
		return err
	}
	if err := RemoveRecentMessage(accountName, messageID); err != nil {
		c.shared.recentCleanupWarningOnce.Do(func() {
			c.warn(fmt.Sprintf("message was deleted, but recent-message history could not be updated (%v). Run mail-app-cli recent clear to remove stale entries.", err))
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
		} else if !isMessageNotFoundError(fallbackErr) {
			return fallbackErr
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

// ResolveArchiveMailbox is read-only so callers can durably record the
// intended destination before Mail is asked to move anything.
func (c *Client) ResolveArchiveMailbox(accountName string) (string, error) {
	if c.nativeEnabled() {
		return c.nativeResolveArchive(accountName)
	}
	script := archiveMailboxLookupScript + fmt.Sprintf(`
tell application "Mail"
 set targetAccount to account "%s"
 set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "All Mail")
 if archiveMailbox is missing value then set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "Archive")
 if archiveMailbox is missing value then error "Archive mailbox not found"
 return name of archiveMailbox
end tell
`, escapeAppleScriptString(accountName))
	output, err := c.runAppleScript(script)
	if err != nil {
		return "", archiveBridgeError(err)
	}
	destination := strings.TrimSpace(output)
	if destination != "All Mail" && destination != "Archive" {
		return "", fmt.Errorf("archive destination resolution returned an invalid mailbox: %q", destination)
	}
	return destination, nil
}

func archiveBridgeError(err error) error {
	if classified := bridgeError(err.Error()); errors.Is(classified, ErrNotFound) {
		return classified
	}
	return err
}

func (c *Client) ArchiveMessageWithDestination(accountName, mailboxName, messageID string) (string, error) {
	destination, err := c.ResolveArchiveMailbox(accountName)
	if err != nil {
		return "", err
	}
	return destination, c.ArchiveMessageToDestination(accountName, mailboxName, messageID, destination)
}

// ArchiveMessageToDestination uses the destination already persisted in a
// batch receipt; it never chooses a different mailbox after mutation starts.
func (c *Client) ArchiveMessageToDestination(accountName, mailboxName, messageID, destination string) error {
	if destination != "All Mail" && destination != "Archive" {
		return fmt.Errorf("invalid archive destination: %q", destination)
	}
	useNative, err := c.archiveUsesNative(accountName, mailboxName, destination)
	if err != nil {
		return err
	}
	if useNative {
		return c.archiveInboxThroughMail(accountName, messageID)
	}
	if c.nativeEnabled() {
		return c.nativeMove(accountName, mailboxName, messageID, destination, true)
	}
	output, err := c.runAppleScript(archiveMessageScript(accountName, mailboxName, messageID, destination))
	if err != nil {
		return archiveBridgeError(err)
	}
	if strings.Contains(output, "Error") {
		return bridgeError(output)
	}
	if strings.TrimSpace(output) != destination {
		return fmt.Errorf("archive destination readback did not match %q", destination)
	}
	return nil
}

const archiveMailboxLookupScript = `
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
`

func archiveMessageScript(accountName, mailboxName, messageID, destination string) string {
	return archiveMailboxLookupScript + fmt.Sprintf(`
tell application "Mail"
 set targetAccount to account "%s"
 set sourceMailbox to my findMailboxByName(mailboxes of targetAccount, "%s")
 if sourceMailbox is missing value then error "Mailbox not found: %s"
 set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "%s")
 if archiveMailbox is missing value then error "Archive mailbox not found"
 if sourceMailbox is archiveMailbox then return name of archiveMailbox
 set targetId to "%s" as integer
 set targetMessage to first message of sourceMailbox whose id is targetId
 move targetMessage to archiveMailbox
 return name of archiveMailbox
end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(mailboxName), escapeAppleScriptString(mailboxName), escapeAppleScriptString(destination), escapeAppleScriptString(messageID))
}

func (c *Client) MoveMessage(accountName, sourceMailbox, messageID, targetMailbox string) error {
	if c.nativeEnabled() {
		return c.nativeMove(accountName, sourceMailbox, messageID, targetMailbox, false)
	}
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

	return c.runJXAStatus(script)
}
