package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// DoctorProbeTimeout bounds each read-only Mail.app diagnostic. Keeping the
// probes separate lets doctor report partial diagnostics when one Mail.app API
// surface is unavailable.
const DoctorProbeTimeout = 3 * time.Second

var mailAppBundlePath = "/System/Applications/Mail.app"

// CheckMailBridge verifies that the local Mail.app bundle is present without
// issuing an Apple Event. Automation permission is checked independently so a
// denied permission cannot be mistaken for a missing Mail installation.
func (c *Client) CheckMailBridge() error {
	info, err := os.Stat(mailAppBundlePath)
	if err != nil {
		return fmt.Errorf("Mail.app bundle unavailable at %s: %w", mailAppBundlePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Mail.app bundle path is not a directory: %s", mailAppBundlePath)
	}
	return nil
}

// CheckAutomationAccess verifies that the automation bridge can read a
// harmless Mail.app property. It is deliberately separate from the bridge
// probe because an installed application can still deny automation access.
func (c *Client) CheckAutomationAccess(timeout time.Duration) error {
	_, err := c.runJXAWithTimeout(`
// mail-app-cli doctor: automation probe
const mail = Application('Mail');
JSON.stringify({version: mail.version()});
`, timeout)
	return err
}

// CheckAccountAccess verifies read access to the account collection and
// returns only its count, not account names or addresses.
func (c *Client) CheckAccountAccess(timeout time.Duration) (int, error) {
	output, err := c.runJXAWithTimeout(`
// mail-app-cli doctor: account access probe
const mail = Application('Mail');
JSON.stringify({accountCount: mail.accounts().length});
`, timeout)
	if err != nil {
		return 0, err
	}
	var result struct {
		AccountCount int `json:"accountCount"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return 0, fmt.Errorf("failed to parse Mail.app account probe: %w", err)
	}
	return result.AccountCount, nil
}

// RunLiveProbe performs a bounded, read-only query against Mail.app. It is
// intentionally independent of account enumeration so doctor does not infer
// live bridge health from a cached or index-backed result.
func (c *Client) RunLiveProbe(timeout time.Duration) error {
	_, err := c.runJXAWithTimeout(`
// mail-app-cli doctor: live probe
const mail = Application('Mail');
const accounts = mail.accounts();
JSON.stringify({enabledAccountCount: accounts.filter(account => account.enabled()).length});
`, timeout)
	return err
}
