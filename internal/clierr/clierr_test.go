package clierr

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestClassifyMapsTypedErrors(t *testing.T) {
	cases := []struct {
		err  error
		code Code
	}{
		{&mail.AutomationTimeoutError{Engine: "jxa", Timeout: time.Second}, CodeTimeout},
		{&mail.AutomationLockTimeoutError{Engine: "jxa", Timeout: time.Second}, CodeTimeout},
		{&mail.PartialSearchError{}, CodePartial},
		{&mail.NotFoundError{Kind: "message", Name: "1"}, CodeNotFound},
		{fmt.Errorf("wrapped: %w", &mail.NotFoundError{Kind: "account", Name: "x"}), CodeNotFound},
		{errors.New("Error: Message not found"), CodeNotFound},
		{errors.New("jxa error: exit status 1 - execution error: Not authorized to send Apple events to Mail. (-1743)"), CodeUnavailable},
		{errors.New("sqlite3 envelope index query failed: authorization denied"), CodeUnavailable},
		{errors.New("something odd"), CodeInternal},
		{Usage("bad"), CodeUsage},
	}
	for _, tc := range cases {
		got := Classify(tc.err)
		if got.Code != tc.code {
			t.Fatalf("Classify(%v).Code = %q, want %q", tc.err, got.Code, tc.code)
		}
	}
}

func TestExitCodesAreDistinctAndDocumented(t *testing.T) {
	seen := map[int]bool{}
	for _, row := range Table() {
		if seen[row.Exit] {
			t.Fatalf("duplicate exit code %d", row.Exit)
		}
		seen[row.Exit] = true
		if ExitCode(row.Code) != row.Exit {
			t.Fatalf("ExitCode(%q) = %d, table says %d", row.Code, ExitCode(row.Code), row.Exit)
		}
	}
	if ExitCode(Code("unknown")) != 7 {
		t.Fatal("unknown codes should exit 7")
	}
}

func TestWithHintDoesNotMutateOriginal(t *testing.T) {
	base := Usage("missing")
	hinted := base.WithHint("try --help")
	if base.Hint != "" || hinted.Hint != "try --help" {
		t.Fatalf("base hint = %q, hinted = %q", base.Hint, hinted.Hint)
	}
}
