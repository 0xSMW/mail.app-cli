package mail

import (
	"encoding/json"
	"fmt"
)

type SmartMailbox struct {
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	Unread     int    `json:"unreadCount,omitempty"`
	TotalCount int    `json:"totalCount,omitempty"`
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
