package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestRecipientFilterInvalidAddressBeforeMailCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nprintf called > '"+marker+"'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"inbox", "unread", "search", "search invoice", "messages list", "messages inbox", "messages unread", "messages sent", "messages drafts", "messages flagged", "messages trash", "messages junk", "messages scan INBOX"} {
		for _, invalid := range []string{"", "  ", "one@example.test,two@example.test"} {
			args := append(strings.Fields(path), "--recipient", invalid, "--json")
			code, stdout, stderr := run(t, args...)
			var failure output.ErrorEnvelope
			if err := json.Unmarshal([]byte(stderr), &failure); err != nil || code != 1 || failure.Code != "usage" {
				t.Fatalf("%s recipient=%q exit=%d stdout=%s stderr=%s", path, invalid, code, stdout, stderr)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid recipient invoked Mail: %v", err)
	}
}

func TestQuerylessSearchRequiresRecipient(t *testing.T) {
	a := newApplication()
	cmd := a.searchCmd
	for _, query := range [][]string{nil, {""}, {" "}} {
		if err := cmd.Args(cmd, query); err == nil {
			t.Fatalf("queryless search accepted: %#v", query)
		}
	}
	if err := cmd.Flags().Set("recipient", "Person+tag@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Args(cmd, nil); err != nil {
		t.Fatalf("queryless recipient search rejected: %v", err)
	}
	if err := cmd.Args(cmd, []string{"one", "two"}); err == nil {
		t.Fatal("multiple positional queries accepted")
	}
}

func TestRecipientFilterRejectsInvalidPagingBeforeMail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nprintf called > '"+marker+"'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := newApplication()
	for _, path := range []string{"inbox", "unread", "search", "messages list", "messages inbox", "messages unread", "messages sent", "messages drafts", "messages flagged", "messages trash", "messages junk", "messages scan INBOX"} {
		command, _, err := a.rootCmd.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		paging := [][]string{{"--limit", "-1"}}
		if command.Flags().Lookup("offset") != nil {
			paging = append(paging, []string{"--offset", "-1"}, []string{"--limit", strconv.Itoa(int(^uint(0) >> 1)), "--offset", "1"})
		}
		for _, flags := range paging {
			args := append(strings.Fields(path), "--recipient", "target@example.test", "--account", "Example", "--backend", "legacy", "--json")
			args = append(args, flags...)
			code, stdout, stderr := run(t, args...)
			var failure output.ErrorEnvelope
			if err := json.Unmarshal([]byte(stderr), &failure); err != nil || code != 1 || failure.Code != "usage" {
				t.Fatalf("%s paging=%v exit=%d stdout=%s stderr=%s", path, flags, code, stdout, stderr)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid paging invoked Mail: %v", err)
	}
}

func TestRecipientListingPartialPreservesDataAndLiveMetadata(t *testing.T) {
	var stdout, stderr bytes.Buffer
	writer, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "inbox", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	a := newApplication()
	a.writer = writer
	result := mail.SearchResult{Messages: []mail.Message{{ID: "42", ToRecipients: []string{"person@example.test"}}}, Complete: false, FailedMailboxes: []mail.SearchMailboxFailure{{Account: "Example", Mailbox: "INBOX", Error: "recipient unavailable"}}}
	err = a.writeRecipientListing(result, "1 message", map[string]any{"account": "Example"}, false)
	if clierr.Classify(err).Code != clierr.CodePartial {
		t.Fatalf("error = %v, want partial", err)
	}
	var envelope struct {
		OK       bool           `json:"ok"`
		ExitCode int            `json:"exitCode"`
		Data     []mail.Message `json:"data"`
		Meta     struct {
			Source   string                      `json:"source"`
			Complete bool                        `json:"complete"`
			Failed   []mail.SearchMailboxFailure `json:"failedMailboxes"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.ExitCode != 5 || len(envelope.Data) != 1 || envelope.Data[0].ID != "42" || envelope.Meta.Source != "live" || envelope.Meta.Complete || len(envelope.Meta.Failed) != 1 {
		t.Fatalf("unexpected partial output: %s", stdout.String())
	}
}

func TestQuerylessRecipientSearchNeverUsesRecentJournal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if err := mail.RecordRecentMessage(mail.Message{ID: "stale", Account: "Example", Mailbox: "INBOX", ToRecipients: []string{"person@example.test"}}, "show"); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		args := []string{"search", "--recipient", "person@example.test", "--account", "Example", "--mailbox", "INBOX", "--json"}
		wantCode := 5
		if allow {
			args = append(args, "--allow-partial")
			wantCode = 0
		}
		code, stdout, stderr := run(t, args...)
		if code != wantCode || strings.Contains(stdout, "stale") || !strings.Contains(stdout, `"source": "live"`) || !strings.Contains(stdout, `"complete": false`) {
			t.Fatalf("allow=%v exit=%d stdout=%s stderr=%s", allow, code, stdout, stderr)
		}
		if allow {
			var envelope struct {
				Data mail.SearchResult `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.Data.Complete || len(envelope.Data.Messages) != 0 || len(envelope.Data.FailedMailboxes) != 1 {
				t.Fatalf("partial envelope lost coverage: %s (%v)", stdout, err)
			}
		}
	}
}

func TestRecipientQueryHumanPartialIncludesSanitizedScopeFailure(t *testing.T) {
	result := mail.SearchResult{
		Messages:        []mail.Message{{ID: "42", Subject: "confirmed match"}},
		Complete:        false,
		FailedMailboxes: []mail.SearchMailboxFailure{{Account: "Work\x1b", Mailbox: "Archive\n", Error: "recipient header\u202e unavailable"}},
	}
	for _, throughWriter := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		if throughWriter {
			writer, err := output.New(output.FormatPlain, &stdout, &stderr, false, "", "inbox", mail.SchemaVersion)
			if err != nil {
				t.Fatal(err)
			}
			a := newApplication()
			a.writer = writer
			if err := a.writeRecipientListing(result, "1 message", map[string]any{}, true); clierr.Classify(err).Code != clierr.CodePartial {
				t.Fatalf("error = %v, want partial", err)
			}
		} else {
			renderRecipientQuery(result, true)(&output.Printer{Out: &stdout})
		}
		got := stdout.String()
		if !strings.Contains(got, "confirmed match") || !strings.Contains(got, `Work\u001b/Archive\u000a: recipient header\u202e unavailable`) {
			t.Fatalf("missing matched message or scope failure: %q", got)
		}
		if strings.ContainsAny(got, "\x1b\u202e") || strings.Contains(got, "Archive\n") {
			t.Fatalf("unsafe scope failure: %q", got)
		}
	}
}
