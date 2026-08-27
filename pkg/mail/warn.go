package mail

import (
	"fmt"
	"os"
)

// Warn is the default sink for non-fatal warnings (index fallback, content
// budget, journal cleanup). A Client can be given its own with SetWarn.
var Warn = func(message string) {
	fmt.Fprintln(os.Stderr, "mail-app-cli: "+message)
}

// SetWarn routes this client's warnings (and those of every WithContext
// copy) to fn instead of the package default.
func (c *Client) SetWarn(fn func(string)) {
	c.shared.warn = fn
}

func (c *Client) warn(message string) {
	if fn := c.shared.warn; fn != nil {
		fn(message)
		return
	}
	Warn(message)
}
