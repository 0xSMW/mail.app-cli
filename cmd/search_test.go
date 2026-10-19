package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
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

func TestSearchIncompleteLiveResultsNeverUseRecentCache(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		allow          bool
		wantExit       int
	}{
		{name: "bounded partial fails closed", response: `printf '%s' '{"messages":[{"id":"live","account":"Work","mailbox":"INBOX","subject":"invoice"}],"truncated":true,"readFailures":0,"scannedMessages":500,"totalMessages":501}'`, wantExit: 5},
		{name: "bounded partial allowed", response: `printf '%s' '{"messages":[{"id":"live","account":"Work","mailbox":"INBOX","subject":"invoice"}],"truncated":true,"readFailures":0,"scannedMessages":500,"totalMessages":501}'`, allow: true},
		{name: "generic mailbox failure fails closed", response: `printf '%s' 'Mail unavailable' >&2; exit 1`, wantExit: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			script := `#!/bin/sh
case "$*" in
 *"const searchTerms"*) ` + tc.response + ` ;;
 *) printf '%s' '[{"id":"work","name":"Work","enabled":true}]' ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			if err := mail.RecordRecentMessage(mail.Message{ID: "cached", Account: "Work", Mailbox: "INBOX", Subject: "invoice"}, "show"); err != nil {
				t.Fatal(err)
			}
			args := []string{"search", "invoice", "--account", "Work", "--mailbox", "INBOX", "--json"}
			if tc.allow {
				args = append(args, "--allow-partial")
			}
			code, stdout, stderr := run(t, args...)
			if code != tc.wantExit {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, "cached") || strings.Contains(stdout, "recent-message journal") {
				t.Fatalf("cached success replaced incomplete result: %s", stdout)
			}
			if tc.allow && (!strings.Contains(stdout, `"complete": false`) && !strings.Contains(stdout, `"complete":false`)) {
				t.Fatalf("missing incomplete metadata: %s", stdout)
			}
		})
	}
}

func TestSearchPreSearchFailurePreservesRecentCacheFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if err := mail.RecordRecentMessage(mail.Message{ID: "cached", Account: "Work", Mailbox: "INBOX", Subject: "invoice"}, "show"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "search", "invoice", "--json")
	if code != 0 || !strings.Contains(stdout, "cached") || !strings.Contains(stdout, "recent") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}
