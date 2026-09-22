package mail

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestScanPreservesMembershipAndAccountIdentity(t *testing.T) {
	scopes := []SearchMailbox{{"Work", "INBOX"}, {"Work", "All Mail"}, {"Home", "INBOX"}, {"Work", "INBOX"}}
	calls := 0
	result := scanMessages(ScanRequest{Scopes: scopes, Limit: 5}, func(scope SearchMailbox, limit int) ([]Message, error) {
		calls++
		if limit != 6 {
			t.Fatalf("limit = %d", limit)
		}
		return []Message{{ID: "1", Subject: "same local id", Account: scope.Account}}, nil
	})
	if calls != 3 || len(result.Messages) != 2 || !result.Complete || len(result.Messages[0].Mailboxes) != 2 {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
	data, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(data), `"mailboxes":["INBOX","All Mail"]`) || !strings.Contains(string(data), `"toRecipients":[]`) {
		t.Fatalf("membership/recipient shape lost: %s, %v", data, err)
	}
}

func TestScanCannotClaimCompleteOnTruncationOrFailure(t *testing.T) {
	result := scanMessages(ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Spam"}, {"Work", "Archive"}}, Limit: 1}, func(scope SearchMailbox, limit int) ([]Message, error) {
		switch scope.Mailbox {
		case "INBOX":
			return []Message{{ID: "1"}, {ID: "2"}}, nil
		case "Spam":
			return []Message{{ID: "unsafe-partial"}}, errors.New("unavailable")
		default:
			return []Message{{ID: "3"}}, nil
		}
	})
	if result.Complete || !result.Coverage[0].Truncated || result.Coverage[1].Error == "" || result.Coverage[2].Truncated || len(result.Messages) != 2 {
		t.Fatalf("result = %+v", result)
	}
}
