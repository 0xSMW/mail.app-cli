package mail

import (
	"fmt"
	"strings"
)

func (c *Client) SendMessage(accountName, subject, body string, to, cc, bcc, attachments []string) error {
	// Escape all recipients
	var toList, ccList, bccList string
	var escapedTo, escapedCc, escapedBcc []string

	for _, addr := range to {
		escapedTo = append(escapedTo, escapeAppleScriptString(addr))
	}
	toList = strings.Join(escapedTo, `", "`)

	for _, addr := range cc {
		escapedCc = append(escapedCc, escapeAppleScriptString(addr))
	}
	ccList = strings.Join(escapedCc, `", "`)

	for _, addr := range bcc {
		escapedBcc = append(escapedBcc, escapeAppleScriptString(addr))
	}
	bccList = strings.Join(escapedBcc, `", "`)

	// Build attachment code
	var attachCodeBuilder strings.Builder
	for _, attPath := range attachments {
		escapedPath := escapeAppleScriptString(attPath)
		fmt.Fprintf(&attachCodeBuilder, `
			try
				make new attachment with properties {file name:"%s"} at after the last paragraph
			on error
				-- Skip files that can't be attached
			end try
`, escapedPath)
	}
	attachCode := attachCodeBuilder.String()

	// AppleScript block
	script := fmt.Sprintf(`
	tell application "Mail"
		try
			set targetAccount to account "%s"
			set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}

			tell newMessage
				set sender to (item 1 of (email addresses of targetAccount as list))

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
%s
			send
			end tell
			return "Success"
		on error errMsg
			return "Error: " & errMsg
		end try
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(subject), escapeAppleScriptString(body),
		toList,
		ccList, ccList,
		bccList, bccList,
		attachCode)

	_, err := c.runAppleScript(script)
	return err
}
