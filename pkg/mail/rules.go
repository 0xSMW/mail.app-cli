package mail

import (
	"encoding/json"
	"fmt"
)

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
on findMailboxByName(mailboxList, targetName)
	repeat with candidateMailbox in mailboxList
		try
			if name of candidateMailbox is targetName then return candidateMailbox
			set nestedMailbox to my findMailboxByName(mailboxes of candidateMailbox, targetName)
			if nestedMailbox is not missing value then return nestedMailbox
		end try
	end repeat
	return missing value
end findMailboxByName

tell application "Mail"
	set destinationMailbox to missing value
	repeat with acc in accounts
		set shouldInspect to true
		%s
		if shouldInspect then
			try
				if "%s" is "Archive" or "%s" is "All Mail" then
					set destinationMailbox to my findMailboxByName(mailboxes of acc, "All Mail")
					if destinationMailbox is missing value then set destinationMailbox to my findMailboxByName(mailboxes of acc, "Archive")
				else
					set destinationMailbox to my findMailboxByName(mailboxes of acc, "%s")
				end if
				if destinationMailbox is not missing value then exit repeat
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
`, accountFilter, escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.MoveTo), escapeAppleScriptString(input.Name), appleScriptBool(input.Enabled), escapeAppleScriptString(input.FromDomain))
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
