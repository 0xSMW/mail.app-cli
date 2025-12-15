package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client provides an interface to interact with Mail.app via AppleScript
type Client struct{}

// NewClient creates a new Mail.app client
func NewClient() *Client {
	return &Client{}
}

// runAppleScript executes an AppleScript and returns the output
func (c *Client) runAppleScript(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("applescript error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// runJXA executes JavaScript for Automation (JXA) and returns the output
func (c *Client) runJXA(script string) (string, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("jxa error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// Account represents a Mail.app account
type Account struct {
	ID           string
	Name         string
	EmailAddress string
	AccountType  string
	UserName     string
	Enabled      bool
}

// Mailbox represents a Mail.app mailbox
type Mailbox struct {
	Name      string
	UnreadCount int
	TotalCount  int
	Account   string
}

// Message represents an email message
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

// Attachment represents an email attachment
type Attachment struct {
	Name     string
	FileSize int
	MimeType string
}

// GetAccounts retrieves all Mail.app accounts
func (c *Client) GetAccounts() ([]Account, error) {
	script := `
tell application "Mail"
	set accountList to {}
	repeat with acc in accounts
		set accountInfo to {id:id of acc, name:name of acc, emailAddress:(try
			(email addresses of acc)
		on error
			""
		end try), accountType:(try
			(delivery account of acc) as string
		on error
			"unknown"
		end try), userName:user name of acc, enabled:enabled of acc}
		set end of accountList to accountInfo
	end repeat
	return accountList
end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	// Parse AppleScript list output
	accounts, err := c.parseAccounts(output)
	return accounts, err
}

// GetMailboxes retrieves all mailboxes for a specific account
func (c *Client) GetMailboxes(accountName string) ([]Mailbox, error) {
	script := fmt.Sprintf(`
tell application "Mail"
	set mailboxList to {}
	try
		set targetAccount to account "%s"
		repeat with mbox in mailboxes of targetAccount
			set mailboxInfo to {name:(name of mbox), unreadCount:(unread count of mbox), totalCount:(count of messages in mbox), account:(name of targetAccount)}
			set end of mailboxList to mailboxInfo
		end repeat
	end try
	return mailboxList
end tell
`, accountName)

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	mailboxes, err := c.parseMailboxes(output)
	return mailboxes, err
}

// GetAllMailboxes retrieves all mailboxes across all accounts
func (c *Client) GetAllMailboxes() ([]Mailbox, error) {
	script := `
tell application "Mail"
	set mailboxList to {}
	repeat with acc in accounts
		repeat with mbox in mailboxes of acc
			set mailboxInfo to {name:(name of mbox), unreadCount:(unread count of mbox), totalCount:(count of messages in mbox), account:(name of acc)}
			set end of mailboxList to mailboxInfo
		end repeat
	end repeat
	return mailboxList
end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	mailboxes, err := c.parseMailboxes(output)
	return mailboxes, err
}

// GetMessages retrieves messages from a mailbox
func (c *Client) GetMessages(accountName, mailboxName string, limit int) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if msgCount > %d then set msgCount to %d", limit, limit)
	}

	script := fmt.Sprintf(`
tell application "Mail"
	set messageList to {}
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msgCount to count of messages in targetMailbox
		%s

		repeat with i from 1 to msgCount
			set msg to message i of targetMailbox
			set msgInfo to {subject:(subject of msg), sender:(sender of msg), dateSent:(date sent of msg as string), dateReceived:(date received of msg as string), isRead:(read status of msg), isFlagged:(flagged status of msg), messageSize:(message size of msg)}
			set end of messageList to msgInfo
		end repeat
	end try
	return messageList
end tell
`, accountName, mailboxName, limitClause)

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	messages, err := c.parseMessages(output)
	return messages, err
}

// SearchMessages searches for messages matching a query
func (c *Client) SearchMessages(query string, limit int) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if msgCount > %d then set msgCount to %d", limit, limit)
	}

	script := fmt.Sprintf(`
tell application "Mail"
	set messageList to {}
	set foundMessages to (every message whose subject contains "%s" or sender contains "%s" or content contains "%s")
	set msgCount to count of foundMessages
	%s

	repeat with i from 1 to msgCount
		set msg to item i of foundMessages
		try
			set msgInfo to {subject:(subject of msg), sender:(sender of msg), dateSent:(date sent of msg as string), dateReceived:(date received of msg as string), isRead:(read status of msg), isFlagged:(flagged status of msg), messageSize:(message size of msg)}
			set end of messageList to msgInfo
		end try
	end repeat
	return messageList
end tell
`, query, query, query, limitClause)

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	messages, err := c.parseMessages(output)
	return messages, err
}


// MarkMessageAsRead marks a message as read
func (c *Client) MarkMessageAsRead(accountName, mailboxName, messageID string, read bool) error {
	readStatus := "true"
	if !read {
		readStatus = "false"
	}

	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msg to message id "%s" of targetMailbox
		set read status of msg to %s
		return "Success"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, mailboxName, messageID, readStatus)

	_, err := c.runAppleScript(script)
	return err
}

// FlagMessage sets or unsets the flagged status of a message
func (c *Client) FlagMessage(accountName, mailboxName, messageID string, flagged bool) error {
	flagStatus := "true"
	if !flagged {
		flagStatus = "false"
	}

	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msg to message id "%s" of targetMailbox
		set flagged status of msg to %s
		return "Success"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, mailboxName, messageID, flagStatus)

	_, err := c.runAppleScript(script)
	return err
}

// DeleteMessage moves a message to trash
func (c *Client) DeleteMessage(accountName, mailboxName, messageID string) error {
	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msg to message id "%s" of targetMailbox
		delete msg
		return "Success"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, mailboxName, messageID)

	_, err := c.runAppleScript(script)
	return err
}

// SendMessage sends a new email message
func (c *Client) SendMessage(accountName, subject, body string, to, cc, bcc []string) error {
	toList := strings.Join(to, `", "`)
	ccList := strings.Join(cc, `", "`)
	bccList := strings.Join(bcc, `", "`)

	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}

		tell newMessage
			repeat with addr in {"%s"}
				make new to recipient at end of to recipients with properties {address:addr}
			end repeat

			if "%s" is not "" then
				repeat with addr in {"%s"}
					make new cc recipient at end of cc recipients with properties {address:addr}
				end repeat
			end if

			if "%s" is not "" then
				repeat with addr in {"%s"}
					make new bcc recipient at end of bcc recipients with properties {address:addr}
				end repeat
			end if

			send
		end tell
		return "Success"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, subject, body, toList, ccList, ccList, bccList, bccList)

	_, err := c.runAppleScript(script)
	return err
}

// Helper function to parse accounts from AppleScript output
func (c *Client) parseAccounts(output string) ([]Account, error) {
	// AppleScript returns records as comma-separated values
	// This is a simplified parser - may need enhancement based on actual output format
	accounts := []Account{}

	// For now, return empty list - need to see actual output format to implement properly
	// TODO: Implement proper parsing based on AppleScript record format

	return accounts, nil
}

// Helper function to parse mailboxes from AppleScript output
func (c *Client) parseMailboxes(output string) ([]Mailbox, error) {
	mailboxes := []Mailbox{}
	// TODO: Implement proper parsing based on AppleScript record format
	return mailboxes, nil
}

// Helper function to parse messages from AppleScript output
func (c *Client) parseMessages(output string) ([]Message, error) {
	messages := []Message{}
	// TODO: Implement proper parsing based on AppleScript record format
	return messages, nil
}

// GetUnreadCount gets the total unread message count
func (c *Client) GetUnreadCount() (int, error) {
	script := `
tell application "Mail"
	set totalUnread to 0
	repeat with acc in accounts
		repeat with mbox in mailboxes of acc
			set totalUnread to totalUnread + (unread count of mbox)
		end repeat
	end repeat
	return totalUnread
end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return 0, err
	}

	var count int
	fmt.Sscanf(output, "%d", &count)
	return count, nil
}

// Helper to convert AppleScript output to JSON for easier parsing
func (c *Client) executeJXAForJSON(jxaScript string) (string, error) {
	script := fmt.Sprintf(`
%s
JSON.stringify(result)
`, jxaScript)
	return c.runJXA(script)
}

// GetAccountsJSON retrieves accounts as JSON using JXA
func (c *Client) GetAccountsJSON() ([]Account, error) {
	script := `
const mail = Application('Mail');
const accounts = mail.accounts();
const result = [];

for (let i = 0; i < accounts.length; i++) {
	const acc = accounts[i];
	result.push({
		id: acc.id(),
		name: acc.name(),
		emailAddress: acc.emailAddresses().length > 0 ? acc.emailAddresses()[0] : '',
		userName: acc.userName(),
		enabled: acc.enabled()
	});
}

JSON.stringify(result);
`
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var accounts []Account
	if err := json.Unmarshal([]byte(output), &accounts); err != nil {
		return nil, fmt.Errorf("failed to parse accounts JSON: %w", err)
	}

	return accounts, nil
}

// GetMailboxesJSON retrieves mailboxes as JSON using JXA
func (c *Client) GetMailboxesJSON(accountName string) ([]Mailbox, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const accounts = mail.accounts();
	for (let i = 0; i < accounts.length; i++) {
		const acc = accounts[i];
		if (acc.name() === '%s' || '%s' === '') {
			const mailboxes = acc.mailboxes();
			for (let j = 0; j < mailboxes.length; j++) {
				const mbox = mailboxes[j];
				try {
					const msgs = mbox.messages();
					result.push({
						name: mbox.name(),
						unreadCount: mbox.unreadCount(),
						totalCount: msgs ? msgs.length : 0,
						account: acc.name()
					});
				} catch (e) {
					// Skip mailboxes that can't be queried
				}
			}
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, accountName, accountName)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var mailboxes []Mailbox
	if err := json.Unmarshal([]byte(output), &mailboxes); err != nil {
		return nil, fmt.Errorf("failed to parse mailboxes JSON: %w", err)
	}

	return mailboxes, nil
}

// GetMessagesJSON retrieves messages from a mailbox using JXA
func (c *Client) GetMessagesJSON(accountName, mailboxName string, limit int, unreadOnly, flaggedOnly bool, since string) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if (messages.length > %d) messages = messages.slice(0, %d);", limit, limit)
	}

	unreadFilter := ""
	if unreadOnly {
		unreadFilter = "messages = messages.filter(m => !m.readStatus());"
	}

	flaggedFilter := ""
	if flaggedOnly {
		flaggedFilter = "messages = messages.filter(m => m.flaggedStatus());"
	}

	sinceFilter := ""
	if since != "" {
		// Parse the since date and create a filter
		// JXA can parse date strings in various formats
		sinceFilter = fmt.Sprintf("const sinceDate = new Date('%s'); messages = messages.filter(m => { const msgDate = m.dateReceived(); return msgDate && msgDate >= sinceDate; });", since)
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const accounts = mail.accounts();
	for (let i = 0; i < accounts.length; i++) {
		const acc = accounts[i];
		if (acc.name() === '%s') {
			const mailboxes = acc.mailboxes();
			for (let j = 0; j < mailboxes.length; j++) {
				const mbox = mailboxes[j];
				if (mbox.name() === '%s') {
					let messages = mbox.messages();
					%s
					%s
					%s
					%s

					for (let k = 0; k < messages.length; k++) {
						const msg = messages[k];
						try {
							result.push({
								id: String(msg.id()),
								subject: msg.subject() || '',
								sender: msg.sender() || '',
								dateReceived: (msg.dateReceived() || new Date()).toString(),
								dateSent: (msg.dateSent() || new Date()).toString(),
								read: msg.readStatus(),
								flagged: msg.flaggedStatus(),
								messageSize: msg.messageSize(),
								mailbox: mbox.name(),
								account: acc.name()
							});
						} catch (e) {
							// Skip messages that cause errors
						}
					}
					break;
				}
			}
			break;
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, accountName, mailboxName, unreadFilter, flaggedFilter, sinceFilter, limitClause)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse messages JSON: %w", err)
	}

	return messages, nil
}

// GetMessageDetailsJSON retrieves full details of a specific message
func (c *Client) GetMessageDetailsJSON(accountName, mailboxName, messageID string) (*Message, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
let result = null;

try {
	const accounts = mail.accounts();
	for (let i = 0; i < accounts.length; i++) {
		const acc = accounts[i];
		if (acc.name() === '%s') {
			const mailboxes = acc.mailboxes();
			for (let j = 0; j < mailboxes.length; j++) {
				const mbox = mailboxes[j];
				if (mbox.name() === '%s') {
					const messages = mbox.messages();
					for (let k = 0; k < messages.length; k++) {
						const msg = messages[k];
						if (msg.id() === '%s') {
							const toRecipients = [];
							const toRecs = msg.toRecipients();
							for (let t = 0; t < toRecs.length; t++) {
								toRecipients.push(toRecs[t].address());
							}

							const ccRecipients = [];
							const ccRecs = msg.ccRecipients();
							for (let c = 0; c < ccRecs.length; c++) {
								ccRecipients.push(ccRecs[c].address());
							}

							const bccRecipients = [];
							const bccRecs = msg.bccRecipients();
							for (let b = 0; b < bccRecs.length; b++) {
								bccRecipients.push(bccRecs[b].address());
							}

							result = {
								id: String(msg.id()),
								subject: msg.subject() || '',
								sender: msg.sender() || '',
								dateReceived: (msg.dateReceived() || new Date()).toString(),
								dateSent: (msg.dateSent() || new Date()).toString(),
								read: msg.readStatus(),
								flagged: msg.flaggedStatus(),
								messageSize: msg.messageSize(),
								content: msg.content() || '',
								mailbox: mbox.name(),
								account: acc.name(),
								toRecipients: toRecipients,
								ccRecipients: ccRecipients,
								bccRecipients: bccRecipients
							};
							break;
						}
					}
					break;
				}
			}
			break;
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, accountName, mailboxName, messageID)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var message Message
	if err := json.Unmarshal([]byte(output), &message); err != nil {
		return nil, fmt.Errorf("failed to parse message JSON: %w", err)
	}

	return &message, nil
}

// ArchiveMessage moves a message to the Archive mailbox
func (c *Client) ArchiveMessage(accountName, mailboxName, messageID string) error {
	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msg to message id "%s" of targetMailbox

		-- Try to find Archive mailbox
		set archiveMailbox to missing value
		repeat with mbox in mailboxes of targetAccount
			if name of mbox is "Archive" or name of mbox is "All Mail" then
				set archiveMailbox to mbox
				exit repeat
			end if
		end repeat

		if archiveMailbox is not missing value then
			move msg to archiveMailbox
			return "Success"
		else
			return "Error: Archive mailbox not found"
		end if
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, mailboxName, messageID)

	output, err := c.runAppleScript(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// MoveMessage moves a message to a different mailbox
func (c *Client) MoveMessage(accountName, sourceMailbox, messageID, targetMailbox string) error {
	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set sourceBox to mailbox "%s" of targetAccount
		set msg to message id "%s" of sourceBox
		set destBox to mailbox "%s" of targetAccount
		move msg to destBox
		return "Success"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, sourceMailbox, messageID, targetMailbox)

	output, err := c.runAppleScript(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// GetAttachmentsJSON retrieves attachments from a message
func (c *Client) GetAttachmentsJSON(accountName, mailboxName, messageID string) ([]Attachment, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const accounts = mail.accounts();
	for (let i = 0; i < accounts.length; i++) {
		const acc = accounts[i];
		if (acc.name() === '%s') {
			const mailboxes = acc.mailboxes();
			for (let j = 0; j < mailboxes.length; j++) {
				const mbox = mailboxes[j];
				if (mbox.name() === '%s') {
					const messages = mbox.messages();
					for (let k = 0; k < messages.length; k++) {
						const msg = messages[k];
						if (msg.id() === '%s') {
							const attachments = msg.mailAttachments();
							for (let a = 0; a < attachments.length; a++) {
								const att = attachments[a];
								result.push({
									name: att.name(),
									fileSize: att.fileSize(),
									mimeType: att.mimeType() || 'unknown'
								});
							}
							break;
						}
					}
					break;
				}
			}
			break;
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, accountName, mailboxName, messageID)

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

// SaveAttachment saves an attachment to disk
func (c *Client) SaveAttachment(accountName, mailboxName, messageID, attachmentName, savePath string) error {
	script := fmt.Sprintf(`
tell application "Mail"
	try
		set targetAccount to account "%s"
		set targetMailbox to mailbox "%s" of targetAccount
		set msg to message id "%s" of targetMailbox

		repeat with att in mail attachments of msg
			if name of att is "%s" then
				save att in POSIX file "%s"
				return "Success"
			end if
		end repeat

		return "Error: Attachment not found"
	on error errMsg
		return "Error: " & errMsg
	end try
end tell
`, accountName, mailboxName, messageID, attachmentName, savePath)

	output, err := c.runAppleScript(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// SearchMessagesJSON searches for messages across all mailboxes
func (c *Client) SearchMessagesJSON(query string, limit int) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if (result.length > %d) result = result.slice(0, %d);", limit, limit)
	}

	// Escape single quotes in query
	escapedQuery := strings.ReplaceAll(query, "'", "\\'")

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const searchTerm = '%s'.toLowerCase();

try {
	const accounts = mail.accounts();
	for (let i = 0; i < accounts.length; i++) {
		const acc = accounts[i];
		const mailboxes = acc.mailboxes();
		for (let j = 0; j < mailboxes.length; j++) {
			const mbox = mailboxes[j];
			const messages = mbox.messages();

			for (let k = 0; k < messages.length; k++) {
				const msg = messages[k];
				try {
					const subject = (msg.subject() || '').toLowerCase();
					const sender = (msg.sender() || '').toLowerCase();
					const content = (msg.content() || '').toLowerCase();

					if (subject.includes(searchTerm) || sender.includes(searchTerm) || content.includes(searchTerm)) {
						result.push({
							id: String(msg.id()),
							subject: msg.subject() || '',
							sender: msg.sender() || '',
							dateReceived: (msg.dateReceived() || new Date()).toString(),
							dateSent: (msg.dateSent() || new Date()).toString(),
							read: msg.readStatus(),
							flagged: msg.flaggedStatus(),
							messageSize: msg.messageSize(),
							mailbox: mbox.name(),
							account: acc.name()
						});
					}
				} catch (e) {
					// Skip messages that cause errors
				}
			}
		}
	}
} catch (e) {
	// Handle errors gracefully
}

%s

JSON.stringify(result);
`, escapedQuery, limitClause)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}

	return messages, nil
}
