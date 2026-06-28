package mail

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DraftInput struct {
	Account     string   `json:"account"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

type Rule struct {
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Conditions []string `json:"conditions,omitempty"`
	Actions    []string `json:"actions,omitempty"`
}

type RuleInput struct {
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	FromDomain string `json:"fromDomain,omitempty"`
	MoveTo     string `json:"moveTo"`
	Enabled    bool   `json:"enabled"`
}

type SmartMailbox struct {
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	Unread     int    `json:"unreadCount,omitempty"`
	TotalCount int    `json:"totalCount,omitempty"`
}

type Signature struct {
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
	Content string `json:"content,omitempty"`
}

func (c *Client) CreateDraft(input DraftInput) (*Message, error) {
	startedAt := time.Now().Add(-1 * time.Second)
	if input.Account == "" {
		return nil, fmt.Errorf("account is required")
	}
	if len(input.To) == 0 {
		return nil, fmt.Errorf("at least one to recipient is required")
	}
	script := fmt.Sprintf(`
tell application "Mail"
	set targetAccount to account "%s"
	set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}
	tell newMessage
		set sender to (item 1 of (email addresses of targetAccount as list))
		repeat with addr in {%s}
			make new to recipient at end of to recipients with properties {address:addr}
		end repeat
		%s
		%s
	end tell
	close newMessage saving yes
	return "ok"
end tell
`, escapeAppleScriptString(input.Account), escapeAppleScriptString(input.Subject), escapeAppleScriptString(input.Body), appleScriptStringList(input.To), appleScriptRecipientBlock("cc", input.Cc), appleScriptRecipientBlock("bcc", input.Bcc))
	if _, err := c.runAppleScript(script); err != nil {
		return nil, err
	}
	time.Sleep(5 * time.Second)
	if draft, err := c.findDraftBySubjectContentSince(input.Account, input.Subject, input.Body, startedAt); err == nil {
		return draft, nil
	}
	return nil, fmt.Errorf("created draft but could not resolve saved draft metadata")
}

func (c *Client) UpdateDraft(accountName, draftID string, input DraftInput) (*Message, error) {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return nil, err
	}
	details, err := c.GetMessageDetailsJSON(draft.Account, draft.Mailbox, draft.ID)
	if err != nil {
		return nil, err
	}
	replacement := DraftInput{
		Account: draft.Account,
		Subject: details.Subject,
		Body:    details.Content,
		To:      details.ToRecipients,
		Cc:      details.CcRecipients,
		Bcc:     details.BccRecipients,
	}
	if input.Subject != "" {
		replacement.Subject = input.Subject
	}
	if input.Body != "" {
		replacement.Body = input.Body
	}
	if replacement.Subject == details.Subject && replacement.Body == details.Content {
		return draft, nil
	}
	if len(replacement.To) == 0 {
		return nil, fmt.Errorf("draft has no to recipients to preserve")
	}
	updated, err := c.CreateDraft(replacement)
	if err != nil {
		return nil, err
	}
	if err := c.deleteDraftByID(draft.Account, draft.Mailbox, draft.ID); err != nil {
		return nil, fmt.Errorf("created updated draft %s but failed to delete original draft %s: %w", updated.ID, draft.ID, err)
	}
	c.cleanupDraftAutosaves(draftCleanupTarget{
		Account: draft.Account,
		Subject: details.Subject,
		Content: details.Content,
		Since:   draft.DateReceived,
		KeepIDs: map[string]bool{updated.ID: true},
	})
	return updated, nil
}

func (c *Client) GetDraft(accountName, draftID string) (*Message, error) {
	messages, err := c.GetUnifiedMessagesJSON("drafts", 500, 0, true)
	if err != nil {
		return nil, err
	}
	for _, message := range messages {
		if message.ID == draftID && (accountName == "" || message.Account == accountName) {
			return &message, nil
		}
	}
	return nil, fmt.Errorf("draft not found: %s", draftID)
}

func (c *Client) SendDraft(accountName, draftID string) error {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return err
	}
	details, err := c.GetMessageDetailsJSON(draft.Account, draft.Mailbox, draft.ID)
	if err != nil {
		return err
	}
	if len(details.ToRecipients) == 0 {
		return fmt.Errorf("draft has no to recipients")
	}
	if err := c.SendMessage(draft.Account, details.Subject, details.Content, details.ToRecipients, details.CcRecipients, details.BccRecipients, nil); err != nil {
		return err
	}
	if err := c.deleteDraftByID(draft.Account, draft.Mailbox, draft.ID); err != nil {
		return err
	}
	c.cleanupDraftAutosaves(draftCleanupTarget{
		Account: draft.Account,
		Subject: details.Subject,
		Content: details.Content,
		Since:   draft.DateReceived,
	})
	return nil
}

func (c *Client) DeleteDraft(accountName, draftID string) error {
	draft, err := c.GetDraft(accountName, draftID)
	if err != nil {
		return err
	}
	if err := c.deleteDraftByID(draft.Account, draft.Mailbox, draft.ID); err != nil {
		return err
	}
	c.cleanupDraftAutosaves(draftCleanupTarget{
		Account: draft.Account,
		Subject: draft.Subject,
		Content: draft.Content,
		Since:   draft.DateReceived,
	})
	return nil
}

func (c *Client) findDraftBySubject(accountName, subject string) (*Message, error) {
	return c.findDraftBySubjectContentSince(accountName, subject, "", time.Time{})
}

func (c *Client) findDraftBySubjectContentSince(accountName, subject, content string, since time.Time) (*Message, error) {
	messages, err := c.GetUnifiedMessagesJSON("drafts", 50, 0, true)
	if err != nil {
		return nil, err
	}
	var best *Message
	for _, message := range messages {
		if message.Subject != subject || (accountName != "" && message.Account != accountName) {
			continue
		}
		if content != "" && normalizeDraftContent(message.Content) != normalizeDraftContent(content) {
			continue
		}
		if !since.IsZero() {
			messageTime, ok := parseMessageTimestamp(message.DateReceived)
			if !ok || messageTime.Before(since) {
				continue
			}
		}
		if best == nil || message.DateReceived > best.DateReceived {
			copy := message
			best = &copy
		}
	}
	if best == nil {
		return nil, fmt.Errorf("draft not found by subject: %s", subject)
	}
	return best, nil
}

func parseMessageTimestamp(value string) (time.Time, bool) {
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func normalizeDraftContent(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

func (c *Client) deleteDraftByID(accountName, mailboxName, draftID string) error {
	id, err := strconv.Atoi(draftID)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`
tell application "Mail"
	delete (first message of mailbox "%s" of account "%s" whose id is %d)
	return "ok"
end tell
`, escapeAppleScriptString(mailboxName), escapeAppleScriptString(accountName), id)
	_, err = c.runAppleScript(script)
	return err
}

type draftCleanupTarget struct {
	Account string
	Subject string
	Content string
	Since   string
	KeepIDs map[string]bool
}

func (c *Client) cleanupDraftAutosaves(target draftCleanupTarget) {
	since, ok := parseMessageTimestamp(target.Since)
	if !ok {
		since = time.Now().Add(-30 * time.Second)
	}
	time.Sleep(8 * time.Second)
	messages, err := c.GetUnifiedMessagesJSON("drafts", 100, 0, true)
	if err != nil {
		return
	}
	for _, message := range messages {
		if shouldDeleteDraftAutosave(message, target, since) {
			_ = c.deleteDraftByID(message.Account, message.Mailbox, message.ID)
		}
	}
}

func shouldDeleteDraftAutosave(message Message, target draftCleanupTarget, since time.Time) bool {
	if target.KeepIDs[message.ID] {
		return false
	}
	if message.Account != target.Account || message.Subject != target.Subject {
		return false
	}
	if normalizeDraftContent(message.Content) != normalizeDraftContent(target.Content) {
		return false
	}
	messageTime, ok := parseMessageTimestamp(message.DateReceived)
	return !ok || !messageTime.Before(since.Add(-1*time.Second))
}

func (c *Client) ListRules() ([]Rule, error) {
	script := `
const mail = Application('Mail');
const result = [];
try {
	const rules = mail.rules();
	for (let i = 0; i < rules.length; i++) {
		const rule = rules[i];
		const conditions = [];
		const actions = [];
		try {
			const ruleConditions = rule.ruleConditions();
			for (let c = 0; c < ruleConditions.length; c++) {
				const condition = ruleConditions[c];
				const parts = [];
				try { parts.push(String(condition.ruleType())); } catch (e) {}
				try { parts.push(String(condition.qualifier())); } catch (e) {}
				try { parts.push(String(condition.expression())); } catch (e) {}
				conditions.push(parts.filter(Boolean).join(' '));
			}
		} catch (e) {}
		try { if (rule.shouldMoveMessage()) actions.push('move to ' + rule.moveMessage().name()); } catch (e) {}
		try { if (rule.deleteMessage()) actions.push('delete'); } catch (e) {}
		try { if (rule.markRead()) actions.push('mark read'); } catch (e) {}
		try { if (rule.markFlagged()) actions.push('mark flagged'); } catch (e) {}
		result.push({name: rule.name(), enabled: rule.enabled(), conditions: conditions, actions: actions});
	}
} catch (e) {
	JSON.stringify({error: String(e)});
}
JSON.stringify(result);
`
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	if err := json.Unmarshal([]byte(output), &rules); err != nil {
		return nil, fmt.Errorf("failed to parse rules JSON: %w", err)
	}
	return rules, nil
}

func (c *Client) CreateRule(input RuleInput) (*Rule, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("rule name is required")
	}
	if input.MoveTo == "" {
		return nil, fmt.Errorf("target mailbox is required")
	}
	if input.FromDomain == "" {
		return nil, fmt.Errorf("from domain is required")
	}
	accountFilter := ""
	if input.Account != "" {
		accountFilter = fmt.Sprintf(`if name of acc is not "%s" then set shouldInspect to false`, escapeAppleScriptString(input.Account))
	}
	script := fmt.Sprintf(`
tell application "Mail"
	set destinationMailbox to missing value
	repeat with acc in accounts
		set shouldInspect to true
		%s
		if shouldInspect then
			try
				set destinationMailbox to mailbox "%s" of acc
				exit repeat
			end try
		end if
	end repeat
	if destinationMailbox is missing value then error "Target mailbox not found: %s"
	set newRule to make new rule at end of rules with properties {name:"%s", enabled:%s, should move message:true, move message:destinationMailbox, all conditions must be met:true}
	tell newRule
		make new rule condition at end of rule conditions with properties {rule type:from header, qualifier:does contain value, expression:"%s"}
	end tell
	return "ok"
end tell
`, accountFilter, escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.Name), appleScriptBool(input.Enabled), escapeAppleScriptString(input.FromDomain))
	if _, err := c.runAppleScript(script); err != nil {
		return nil, err
	}
	return &Rule{
		Name:       input.Name,
		Enabled:    input.Enabled,
		Conditions: []string{"from contains " + input.FromDomain},
		Actions:    []string{"move to " + input.MoveTo},
	}, nil
}

func (c *Client) SetRuleEnabled(name string, enabled bool) error {
	return c.runNamedCollectionBooleanAction("rules", name, "enabled", enabled)
}

func (c *Client) DeleteRule(name string) error {
	return c.runNamedCollectionDeleteAction("rules", name)
}

func (c *Client) ListSmartMailboxes() ([]SmartMailbox, error) {
	script := `
const mail = Application('Mail');
const result = [];
try {
	const boxes = mail.smartMailboxes ? mail.smartMailboxes() : [];
	for (let i = 0; i < boxes.length; i++) {
		const box = boxes[i];
		let total = 0;
		let unread = 0;
		try { total = box.messages().length; } catch (e) {}
		try { unread = box.unreadCount(); } catch (e) {}
		result.push({name: box.name(), totalCount: total, unreadCount: unread});
	}
} catch (e) {
	JSON.stringify({error: String(e)});
}
JSON.stringify(result);
`
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var boxes []SmartMailbox
	if err := json.Unmarshal([]byte(output), &boxes); err != nil {
		return nil, fmt.Errorf("failed to parse smart mailboxes JSON: %w", err)
	}
	return boxes, nil
}

func (c *Client) ListSignatures(includeContent bool) ([]Signature, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
try {
	const signatures = mail.signatures ? mail.signatures() : [];
	for (let i = 0; i < signatures.length; i++) {
		const sig = signatures[i];
		const item = {name: sig.name()};
		if (%t) {
			try { item.content = sig.content(); } catch (e) { item.content = ''; }
		}
		result.push(item);
	}
} catch (e) {
	JSON.stringify({error: String(e)});
}
JSON.stringify(result);
`, includeContent)
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var signatures []Signature
	if err := json.Unmarshal([]byte(output), &signatures); err != nil {
		return nil, fmt.Errorf("failed to parse signatures JSON: %w", err)
	}
	sort.Slice(signatures, func(i, j int) bool { return signatures[i].Name < signatures[j].Name })
	return signatures, nil
}

func (c *Client) SignatureByName(name string) (*Signature, error) {
	signatures, err := c.ListSignatures(true)
	if err != nil {
		return nil, err
	}
	for _, signature := range signatures {
		if signature.Name == name {
			return &signature, nil
		}
	}
	return nil, fmt.Errorf("signature not found: %s", name)
}

func (c *Client) runNamedCollectionBooleanAction(collection, name, property string, value bool) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = {ok: false};
try {
	const items = mail.%s();
	for (let i = 0; i < items.length; i++) {
		if (items[i].name() === '%s') {
			items[i].%s = %s;
			result.ok = true;
			break;
		}
	}
} catch (e) {
	result.error = String(e);
}
JSON.stringify(result);
`, collection, escapeJSString(name), property, jxaBool(value))
	output, err := c.runJXA(script)
	return parseMutationResult(output, err, name)
}

func (c *Client) runNamedCollectionDeleteAction(collection, name string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = {ok: false};
try {
	const items = mail.%s();
	for (let i = 0; i < items.length; i++) {
		if (items[i].name() === '%s') {
			items[i].delete();
			result.ok = true;
			break;
		}
	}
} catch (e) {
	result.error = String(e);
}
JSON.stringify(result);
`, collection, escapeJSString(name))
	output, err := c.runJXA(script)
	return parseMutationResult(output, err, name)
}

func parseMutationResult(output string, runErr error, name string) error {
	if runErr != nil {
		return runErr
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return err
	}
	if result.Error != "" {
		return fmt.Errorf(result.Error)
	}
	if !result.OK {
		return fmt.Errorf("item not found: %s", name)
	}
	return nil
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
