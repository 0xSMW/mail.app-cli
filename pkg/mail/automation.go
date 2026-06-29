package mail

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func escapeJSString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "'", "\\'")   // Escape single quotes
	s = strings.ReplaceAll(s, "\n", "\\n")  // Escape newlines
	s = strings.ReplaceAll(s, "\r", "\\r")  // Escape carriage returns
	s = strings.ReplaceAll(s, "\t", "\\t")  // Escape tabs
	return s
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "\"", "\\\"") // Escape double quotes
	return s
}

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

func (c *Client) runJXAWithTimeout(script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("jxa timed out after %s", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("jxa error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

func jxaBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func jxaMailboxLookupExpression(mailboxName string) string {
	return jxaMailboxLookupExpressionFor(mailboxName, "requestedMailbox")
}

func jxaMailboxLookupExpressionFor(mailboxName, variableName string) string {
	if isArchiveAlias(mailboxName) {
		return fmt.Sprintf("(findMailboxByNames(acc.mailboxes(), ['All Mail', 'Archive']) || acc.mailboxes.byName(%s))", variableName)
	}
	return fmt.Sprintf("(findMailboxByNames(acc.mailboxes(), [%s]) || acc.mailboxes.byName(%s))", variableName, variableName)
}

func jxaMailboxLookupHelper() string {
	return `
function findMailboxByNames(mailboxes, names) {
	for (let i = 0; i < mailboxes.length; i++) {
		const mailbox = mailboxes[i];
		try {
			if (names.includes(mailbox.name())) {
				return mailbox;
			}
			const child = findMailboxByNames(mailbox.mailboxes(), names);
			if (child !== null) {
				return child;
			}
		} catch (e) {}
	}
	return null;
}
`
}

func jxaMessageByIdHelper() string {
	return `
function messageById(mbox, messageId) {
	let msg = null;
	try {
		msg = mbox.messages.byId(Number(messageId));
		msg.id();
		return msg;
	} catch (e) {
		msg = null;
	}
	try {
		const allIds = mbox.messages.id();
		const targetIdx = allIds.findIndex(id => String(id) === messageId);
		if (targetIdx >= 0) {
			return mbox.messages.at(targetIdx);
		}
	} catch (e) {}
	return null;
}
`
}

func appleScriptBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func appleScriptStringList(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, `"`+escapeAppleScriptString(value)+`"`)
	}
	return strings.Join(escaped, ", ")
}

func appleScriptRecipientBlock(kind string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	return fmt.Sprintf(`
		repeat with addr in {%s}
			make new %s recipient at end of %s recipients with properties {address:addr}
		end repeat
`, appleScriptStringList(values), kind, kind)
}
