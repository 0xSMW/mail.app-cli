package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestPartialSearchOutputDataUsesMessagesForListModifiers(t *testing.T) {
	result := mail.SearchResult{
		Messages: []mail.Message{{ID: "10"}, {ID: "20"}},
		Complete: false,
		FailedMailboxes: []mail.SearchMailboxFailure{{
			Account: "Work",
			Mailbox: "INBOX",
			Error:   "unavailable",
		}},
	}

	for _, tc := range []struct {
		name   string
		format output.Format
		want   string
	}{
		{name: "count", format: output.FormatCount, want: "2"},
		{name: "ids only", format: output.FormatIDs, want: "10\n20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			writer, err := output.New(tc.format, &stdout, &stderr, false, "", "search", mail.SchemaVersion)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Write(output.Result{Data: partialSearchOutputData(result, tc.format)}); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(stdout.String()); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPartialSearchOutputDataPreservesMetadataForJSON(t *testing.T) {
	result := mail.SearchResult{
		Messages: []mail.Message{{ID: "10"}},
		Complete: false,
		FailedMailboxes: []mail.SearchMailboxFailure{{
			Account: "Work",
			Mailbox: "INBOX",
			Error:   "unavailable",
		}},
	}

	data, ok := partialSearchOutputData(result, output.FormatJSON).(mail.SearchResult)
	if !ok {
		t.Fatalf("JSON data = %T, want mail.SearchResult", partialSearchOutputData(result, output.FormatJSON))
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"complete":false`) || !strings.Contains(string(raw), `"failedMailboxes"`) {
		t.Fatalf("JSON data lost partial-search metadata: %s", raw)
	}
}
