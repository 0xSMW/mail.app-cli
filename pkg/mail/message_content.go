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
	c.shared.contentWarningOnce.Do(func() {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown error"
		}
		c.warn(fmt.Sprintf("message content fetch was limited (%s). Returned message metadata with any content that could be fetched in time. Use a smaller --limit or show for full content on specific messages.", reason))
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
	complete, diskErr := c.enrichMessagesFromDisk(accountName, mailboxName, messages)
	if diskErr != nil && (c.Done() != nil || (c.nativeEnabled() && !isEnvelopeIndexUnavailable(diskErr))) {
		c.warnContentFallback(diskErr)
		for i := range messages {
			messages[i].ContentError = diskErr.Error()
		}
		return messages
	}
	type pendingScope struct {
		account, mailbox string
		indices          []int
	}
	var groups []pendingScope
	scopePositions := make(map[string]int)
	for i := range messages {
		if complete[i] {
			continue
		}
		a, m := accountName, mailboxName
		if a == "" {
			a = messages[i].Account
		}
		if m == "" {
			m = messages[i].Mailbox
		}
		key := a + "\x00" + m
		position, exists := scopePositions[key]
		if !exists {
			position = len(groups)
			scopePositions[key] = position
			groups = append(groups, pendingScope{account: a, mailbox: m})
		}
		groups[position].indices = append(groups[position].indices, i)
	}

	for _, group := range groups {
		pending := group.indices
		for start := 0; start < len(pending); start += chunkSize {
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
			if end > len(pending) {
				end = len(pending)
			}
			ids := make([]string, 0, end-start)
			for _, i := range pending[start:end] {
				ids = append(ids, messages[i].ID)
			}
			contentByID, err := c.getMessageContentBatch(group.account, group.mailbox, ids, chunkTimeout)
			if err != nil {
				c.warnContentFallback(err)
				for _, i := range pending[start:end] {
					messages[i].ContentError = err.Error()
				}
				return messages
			}
			for _, i := range pending[start:end] {
				if content, ok := contentByID[messages[i].ID]; ok {
					messages[i].Content = content
					messages[i].ContentSource = BodySourceMail
					messages[i].ContentError = ""
				}
			}
		}
	}

	return messages
}

func (c *Client) getMessageContentBatch(accountName, mailboxName string, ids []string, timeout time.Duration) (map[string]string, error) {
	if c.nativeEnabled() {
		return c.nativeMessageContentBatch(accountName, mailboxName, ids, timeout)
	}
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
