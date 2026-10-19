package mail

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyReadDecodeAndAbsenceContracts(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	for _, response := range []string{"null", "{}", `{"id":"42"}`} {
		t.Run(response, func(t *testing.T) {
			writeFakeOsaScript(t, "printf '%s\\n' '"+response+"'\n")
			message, err := NewClient().GetMessageStateForVerificationJSON("Work", "INBOX", "42")
			if err != nil || (message == nil) != (response == "null") {
				t.Fatalf("verification read = %+v, %v", message, err)
			}
			envelope, err := NewClient().getMessageEnvelopeJXA("Work", "INBOX", "42")
			if err != nil || (envelope == nil) != (response != `{"id":"42"}`) {
				t.Fatalf("envelope = %+v, %v", envelope, err)
			}
		})
	}
	writeFakeOsaScript(t, "printf 'invalid\\n'\n")
	_, err := NewClient().ListRules()
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "failed to parse rules JSON: ") {
		t.Fatalf("rules error = %v", err)
	}
	_, err = NewClient().GetMessageStateForVerificationJSON("Work", "INBOX", "42")
	if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "failed to parse verification state JSON: ") {
		t.Fatalf("verification error = %v", err)
	}
}

func TestLegacyStatusPreservesBridgeErrors(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	for _, response := range []string{"Success", "Error: Message not found", "Error: not authorized (-1743)"} {
		t.Run(response, func(t *testing.T) {
			writeFakeOsaScript(t, "printf '%s\\n' '"+response+"'\n")
			err := NewClient().MarkMessageAsRead("Work", "INBOX", "42", true)
			if response == "Success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if want := bridgeError(response); err == nil || err.Error() != want.Error() || errors.Is(err, ErrNotFound) != errors.Is(want, ErrNotFound) {
				t.Fatalf("status error = %v, want %v", err, want)
			}
		})
	}
}

func TestLegacyJSONExecutionPreservesTimeout(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, "/bin/sleep 2\n")
	var rules []Rule
	err := NewClient().runJXAJSON("script", 30*time.Millisecond, "rules", &rules)
	var timeout *AutomationTimeoutError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &timeout) || timeout.Timeout != 30*time.Millisecond || rules != nil {
		t.Fatalf("timeout = %+v, err = %v, rules = %v", timeout, err, rules)
	}
}
