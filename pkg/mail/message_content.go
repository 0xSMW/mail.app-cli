package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func (c *Client) warnContentFallback(err error) {
	c.contentWarningOnce.Do(func() {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown error"
		}
		fmt.Fprintf(os.Stderr, "mail-app-cli: message content fetch was limited (%s). Returned message metadata with any content that could be fetched in time. Use a smaller --limit or messages show for full content on specific messages.\n", reason)
	})
}

func mailContentFetchBudget() time.Duration {
	const defaultBudget = 45 * time.Second
	raw := strings.TrimSpace(os.Getenv("MAIL_APP_CLI_CONTENT_TIMEOUT"))
	if raw == "" {
		return defaultBudget
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultBudget
	}
	return time.Duration(seconds) * time.Second
}

func (c *Client) enrichMessagesWithContent(accountName, mailboxName string, messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	const chunkSize = 10
	deadline := time.Now().Add(mailContentFetchBudget())

	for start := 0; start < len(messages); start += chunkSize {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.warnContentFallback(fmt.Errorf("content fetch budget exhausted"))
			return messages
		}
		chunkTimeout := remaining
		if chunkTimeout > 10*time.Second {
			chunkTimeout = 10 * time.Second
		}

		end := start + chunkSize
		if end > len(messages) {
			end = len(messages)
		}

		ids := make([]string, 0, end-start)
		for _, message := range messages[start:end] {
			ids = append(ids, message.ID)
		}
		contentByID, err := c.getMessageContentBatch(accountName, mailboxName, ids, chunkTimeout)
		if err != nil {
			c.warnContentFallback(err)
			return messages
		}
		for i := start; i < end; i++ {
			if content, ok := contentByID[messages[i].ID]; ok {
				messages[i].Content = content
			}
		}
	}

	return messages
}

func (c *Client) getMessageContentBatch(accountName, mailboxName string, ids []string, timeout time.Duration) (map[string]string, error) {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
const messageIds = %s;
const result = {};
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	for (let i = 0; i < messageIds.length; i++) {
		const id = String(messageIds[i]);
		const msg = messageById(mbox, id);
		if (msg === null) {
			result[id] = '';
			continue;
		}
		try {
			result[id] = msg.content() || '';
		} catch(e) {
			result[id] = '';
		}
	}
} catch(e) {}

JSON.stringify(result);
`, escapeJSString(mailboxName), string(encodedIDs), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXAWithTimeout(script, timeout)
	if err != nil {
		return nil, err
	}

	var contentByID map[string]string
	if err := json.Unmarshal([]byte(output), &contentByID); err != nil {
		return nil, fmt.Errorf("failed to parse content JSON: %w", err)
	}
	return contentByID, nil
}
