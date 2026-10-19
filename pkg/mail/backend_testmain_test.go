package mail

import (
	"os"
	"testing"
)

// Existing scripting fixtures explicitly exercise the compatibility backend.
// Native tests select their backend and inject a descriptor transport.
func TestMain(m *testing.M) {
	if os.Getenv("MAIL_APP_CLI_BACKEND") == "" {
		_ = os.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	}
	os.Exit(m.Run())
}
