package mail

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestJXABool(t *testing.T) {
	if got := jxaBool(true); got != "true" {
		t.Fatalf("jxaBool(true) = %q, want %q", got, "true")
	}
	if got := jxaBool(false); got != "false" {
		t.Fatalf("jxaBool(false) = %q, want %q", got, "false")
	}
}

func TestEscapeJSString(t *testing.T) {
	input := "quote' slash\\ line\n tab\t café 😀 \u2028\u2029"
	want := `quote\' slash\\ line\n tab\t caf\u00E9 \uD83D\uDE00 \u2028\u2029`
	if got := escapeJSString(input); got != want {
		t.Fatalf("escapeJSString = %q, want %q", got, want)
	}
}

func TestJXAMailboxLookupUsesInboxAccessor(t *testing.T) {
	helper := jxaMailboxLookupHelper()
	for _, want := range []string{"function findMailbox(acc, requestedName, names)", "return acc.inbox()"} {
		if !strings.Contains(helper, want) {
			t.Fatalf("jxaMailboxLookupHelper missing %q", want)
		}
	}

	if got := jxaMailboxLookupExpression("INBOX"); got != "findMailbox(acc, requestedMailbox, [requestedMailbox])" {
		t.Fatalf("jxaMailboxLookupExpression(INBOX) = %q", got)
	}
	if got := jxaMailboxLookupExpression("Archive"); got != "findMailbox(acc, requestedMailbox, ['All Mail', 'Archive'])" {
		t.Fatalf("jxaMailboxLookupExpression(Archive) = %q", got)
	}
}

func TestArchiveAliasHelpers(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Archive", true},
		{"All Mail", true},
		{"[Gmail]/All Mail", true},
		{"gmail/all mail", true},
		{"INBOX", false},
		{"GitHub", false},
	}
	for _, tt := range tests {
		if got := isArchiveAlias(tt.name); got != tt.want {
			t.Fatalf("isArchiveAlias(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestIndexMailboxURLPattern(t *testing.T) {
	if got := indexMailboxURLPattern("abc-123", "Archive"); got != "imap://abc-123/%5BGmail%5D/All%20Mail" {
		t.Fatalf("archive URL = %q", got)
	}
	if got := indexMailboxURLPattern("abc-123", "All Mail"); got != "imap://abc-123/%5BGmail%5D/All%20Mail" {
		t.Fatalf("all mail URL = %q", got)
	}
	if got := indexMailboxURLPattern("abc-123", "GitHub Updates"); got != "imap://abc-123/GitHub%20Updates" {
		t.Fatalf("regular mailbox URL = %q", got)
	}
}

func TestMailboxLeafFromURL(t *testing.T) {
	got := mailboxLeafFromURL("imap://abc/%5BGmail%5D/All%20Mail")
	if got != "All Mail" {
		t.Fatalf("mailboxLeafFromURL = %q, want All Mail", got)
	}
}

func TestSQLQuote(t *testing.T) {
	got := sqlQuote("Bob's [Gmail]")
	if got != "'Bob''s [Gmail]'" {
		t.Fatalf("sqlQuote = %q", got)
	}
}

func TestEscapeSQLLikePattern(t *testing.T) {
	got := escapeSQLLikePattern(`100%_done\ok`)
	want := `100\%\_done\\ok`
	if got != want {
		t.Fatalf("escapeSQLLikePattern = %q, want %q", got, want)
	}
}

func TestIndexMailboxMembershipCondition(t *testing.T) {
	regular := indexMailbox{ID: 42, Name: "INBOX"}
	got := indexMailboxMembershipCondition(&regular)
	for _, want := range []string{
		"m.mailbox = 42",
		"labels l",
		"l.mailbox_id = 42",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("regular membership condition %q does not contain %q", got, want)
		}
	}

	archive := indexMailbox{ID: 99, Name: "All Mail"}
	if got := indexMailboxMembershipCondition(&archive); got != "m.mailbox = 99" {
		t.Fatalf("archive membership condition = %q, want direct mailbox check", got)
	}
}

func TestIsSpecialMailboxNameIncludesSentItems(t *testing.T) {
	if !isSpecialMailboxName("sent", "Sent Items") {
		t.Fatal("Sent Items should be treated as a sent mailbox")
	}
}

func TestRunBulkOperations(t *testing.T) {
	t.Run("zero requests", func(t *testing.T) {
		called := false
		err := runBulkOperations([]int{}, "failed", func(int) error {
			called = true
			return nil
		})
		if err != nil {
			t.Fatalf("runBulkOperations returned error: %v", err)
		}
		if called {
			t.Fatal("runBulkOperations called callback for zero requests")
		}
	})

	t.Run("single request", func(t *testing.T) {
		var calls []int
		err := runBulkOperations([]int{7}, "failed", func(value int) error {
			calls = append(calls, value)
			return nil
		})
		if err != nil {
			t.Fatalf("runBulkOperations returned error: %v", err)
		}
		if len(calls) != 1 || calls[0] != 7 {
			t.Fatalf("callback calls = %v, want [7]", calls)
		}
	})

	t.Run("multiple request errors", func(t *testing.T) {
		err := runBulkOperations([]int{1, 2, 3}, "failed to process", func(value int) error {
			if value == 2 {
				return nil
			}
			return fmt.Errorf("request %d failed", value)
		})
		if err == nil {
			t.Fatal("runBulkOperations returned nil error")
		}

		message := err.Error()
		for _, want := range []string{
			"failed to process:",
			"request 1 failed",
			"request 3 failed",
		} {
			if !strings.Contains(message, want) {
				t.Fatalf("error %q does not contain %q", message, want)
			}
		}
	})
}

func TestSortAndSliceUsesGlobalDateOrder(t *testing.T) {
	messages := []Message{
		{ID: "1", DateReceived: "2026-06-20T10:00:00Z"},
		{ID: "2", DateReceived: "2026-06-22T10:00:00Z"},
		{ID: "3", DateReceived: "2026-06-21T10:00:00Z"},
		{ID: "4", DateReceived: "2026-06-19T10:00:00Z"},
	}

	got := sortAndSlice(messages, 1, 2)
	gotIDs := []string{got[0].ID, got[1].ID}
	wantIDs := []string{"3", "1"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("sortAndSlice ids = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestIsEnvelopeIndexUnavailable(t *testing.T) {
	unavailableErrors := []error{
		errors.New(`sqlite3 envelope index query failed: exit status 1 - Error: unable to open database "/Users/example/Library/Mail/V10/MailData/Envelope Index": authorization denied`),
		errors.New("ls: MailData: Operation not permitted"),
		errors.New("sqlite3: executable file not found"),
		errors.New("no such file"),
		errors.New("envelope index disabled"),
	}

	for _, err := range unavailableErrors {
		if !isEnvelopeIndexUnavailable(err) {
			t.Fatalf("isEnvelopeIndexUnavailable(%q) = false, want true", err)
		}
	}

	if isEnvelopeIndexUnavailable(errors.New("failed to parse envelope index JSON")) {
		t.Fatal("parse errors should not be treated as unavailable index")
	}
}
