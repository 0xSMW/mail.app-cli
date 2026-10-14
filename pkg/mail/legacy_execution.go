package mail

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// runJXAJSON keeps execution failures intact and gives decode failures the
// operation's existing context. Callers choose the same timeout as their read.
func (c *Client) runJXAJSON(script string, timeout time.Duration, label string, destination any) error {
	output, err := c.runJXAWithTimeout(script, timeout)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(output), destination); err != nil {
		return fmt.Errorf("failed to parse %s JSON: %w", label, err)
	}
	return nil
}

// runJXAStatus handles the string status contract of legacy message actions.
func (c *Client) runJXAStatus(script string) error {
	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return bridgeError(output)
	}
	return nil
}

// runJXAMessage preserves the explicit null response used by optional reads.
func (c *Client) runJXAMessage(script string, timeout time.Duration, label string) (*Message, error) {
	output, err := c.runJXAWithTimeout(script, timeout)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "null" {
		return nil, nil
	}
	var message Message
	if err := json.Unmarshal([]byte(output), &message); err != nil {
		return nil, fmt.Errorf("failed to parse %s JSON: %w", label, err)
	}
	return &message, nil
}
