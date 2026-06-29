package mail

import (
	"encoding/json"
	"fmt"
	"sort"
)

type Signature struct {
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
	Content string `json:"content,omitempty"`
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
