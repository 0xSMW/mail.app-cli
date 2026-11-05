package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestRecipientFlagsOnReadCommands(t *testing.T) {
	a := newApplication()
	for _, path := range []string{"inbox", "unread", "search", "messages list", "messages inbox", "messages unread", "messages sent", "messages drafts", "messages flagged", "messages trash", "messages junk", "messages scan"} {
		command, _, err := a.rootCmd.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		flag := command.Flags().Lookup("with-recipients")
		if flag == nil || flag.DefValue != "false" {
			t.Fatalf("%s missing opt-in recipient flag", path)
		}
	}
}

func TestRenderMessagesRecipientLinesAndNoFlagCompatibility(t *testing.T) {
	longAddress := strings.Repeat("a", 100) + "@example.test"
	message := mail.Message{ID: "1", Subject: "sample", Sender: "sender@example.test", Read: true, ToRecipients: []string{longAddress, "second@example.test"}, DeliveredTo: []string{"alias@example.test"}}
	message.RecipientStatus.To = mail.RecipientFieldStatus{State: "available", Source: "disk"}
	message.RecipientStatus.Cc = mail.RecipientFieldStatus{State: "available", Source: "disk"}
	message.RecipientStatus.Bcc = mail.RecipientFieldStatus{State: "unavailable"}
	message.RecipientStatus.DeliveredTo = mail.RecipientFieldStatus{State: "available", Source: "disk"}
	message.RecipientStatus.OriginalTo = mail.RecipientFieldStatus{State: "available", Source: "disk"}
	var old, explicitFalse, enriched bytes.Buffer
	renderMessages([]mail.Message{message}, false)(&output.Printer{Out: &old})
	renderMessages([]mail.Message{message}, false, false)(&output.Printer{Out: &explicitFalse})
	if old.String() != explicitFalse.String() || strings.Contains(old.String(), "To:") {
		t.Fatalf("no-flag output changed: %q", explicitFalse.String())
	}
	renderMessages([]mail.Message{message}, false, true)(&output.Printer{Out: &enriched})
	for _, want := range []string{longAddress + ", second@example.test", "Cc: (none in stored headers)", "Bcc: (unavailable)", "Delivered-To: alias@example.test", "X-Original-To: (none in stored headers)"} {
		if !strings.Contains(enriched.String(), want) {
			t.Fatalf("missing %q in %s", want, enriched.String())
		}
	}
}

func TestRecipientHumanOutputEscapesControls(t *testing.T) {
	message := mail.Message{ID: "1", Subject: "sample", ToRecipients: []string{"test\x1b[31m\n\u202e@example.test"}}
	message.RecipientStatus.To = mail.RecipientFieldStatus{State: "available"}
	for _, color := range []bool{false, true} {
		var buffer bytes.Buffer
		renderMessages([]mail.Message{message}, false, true)(&output.Printer{Out: &buffer, Color: color})
		got := buffer.String()
		if strings.ContainsRune(got, '\u202e') || strings.Contains(got, "test\x1b") || !strings.Contains(got, `test\u001b[31m\u000a\u202e@example.test`) {
			t.Fatalf("unsafe or missing recipient output: %q", got)
		}
	}
}

func TestSearchRecipientFallbackDoesNotTrustJournalRecipients(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if err := mail.RecordRecentMessage(mail.Message{ID: "100001", Account: "Example Account", Mailbox: "INBOX", Subject: "invoice", ToRecipients: []string{"stale@example.test"}}, "show"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "search", "invoice", "--with-recipients", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "stale@example.test") {
		t.Fatalf("stale recipient retained: %s", stdout)
	}
	var result struct {
		Data []mail.Message `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || result.Data[0].RecipientStatus.To.State != "unavailable" {
		t.Fatalf("missing unavailable status: %s", stdout)
	}
}

func TestCachedListRecipientReadKeepsListingProvenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	cached, err := cache.New()
	if err != nil {
		t.Fatal(err)
	}
	const key = "msgs-Example-Account-INBOX-50-0-false-false--false"
	if err := cached.Set(key, []mail.Message{{ID: "100001", Account: "Example Account", Mailbox: "INBOX", Subject: "sample", ToRecipients: []string{"stale@example.test"}}}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "messages", "list", "--account", "Example Account", "--mailbox", "INBOX", "--limit", "50", "--with-recipients", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	var result struct {
		Data []mail.Message `json:"data"`
		Meta struct {
			Source string `json:"source"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Meta.Source != "cache" || len(result.Data) != 1 || result.Data[0].RecipientStatus.To.State != "unavailable" || len(result.Data[0].ToRecipients) != 0 {
		t.Fatalf("unexpected cached recipient result: %s", stdout)
	}
	var stored []mail.Message
	if found, err := cached.Get(key, &stored); err != nil || !found || len(stored) != 1 || len(stored[0].ToRecipients) != 1 {
		t.Fatalf("recipient read mutated cached envelopes: %#v, found=%v, error=%v", stored, found, err)
	}
}

func TestMetadataOnlyRenderIncludesHeaderStatusWithoutBody(t *testing.T) {
	message := mail.Message{ID: "100001", Content: "body must stay hidden", DeliveredTo: []string{"alias@example.test"}, OriginalTo: []string{"original@example.test"}}
	message.RecipientStatus.To = mail.RecipientFieldStatus{State: "unavailable"}
	message.RecipientStatus.Cc = mail.RecipientFieldStatus{State: "available"}
	message.RecipientStatus.DeliveredTo = mail.RecipientFieldStatus{State: "available"}
	message.RecipientStatus.OriginalTo = mail.RecipientFieldStatus{State: "available"}
	var buffer bytes.Buffer
	renderMessage(&message, true)(&output.Printer{Out: &buffer})
	got := buffer.String()
	for _, want := range []string{"(unavailable)", "(none in stored headers)", "Delivered-To:", "alias@example.test", "X-Original-To:", "original@example.test"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, message.Content) {
		t.Fatalf("metadata rendered body: %s", got)
	}
}
