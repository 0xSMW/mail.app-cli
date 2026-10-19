package mail

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
)

func localReadFixture(t *testing.T) (*Client, string, *atomic.Int64) {
	t.Helper()
	if !mailindex.Available() {
		t.Skip("embedded SQLite unavailable")
	}
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is needed only to create a synthetic fixture")
	}
	root := filepath.Join(t.TempDir(), "邮件 café", "V12")
	path := filepath.Join(root, "MailData", "Envelope Index")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sql := `create table mailboxes(url text,total_count integer,unread_count integer);
insert into mailboxes(ROWID,url,total_count,unread_count) values
(1,'imap://ABC/%5BGmail%5D/All%20Mail',5,5),(2,'imap://ABC/INBOX',5,5),(3,'imap://OTHER/INBOX',1,1),(4,'imap://ABC/%E9%82%AE%E4%BB%B6%20caf%C3%A9',1,1);
create table subjects(subject text); insert into subjects values ('indexed subject');
create table addresses(address text,comment text); insert into addresses values ('sender@example.com','Sender');
create table summaries(summary text); insert into summaries values ('indexed summary');
create table messages(subject integer,sender integer,summary integer,mailbox integer,date_received integer,date_sent integer,read integer,flagged integer,deleted integer,size integer,subject_prefix text default '');
insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values
(101,1,1,1,1,1690000000,1690000000,0,1,0,1234),
(102,1,1,1,3,1690000000,1690000000,0,0,0,1234),
(103,1,1,1,1,1690000000,1690000000,0,0,0,1234),
(104,1,1,1,1,1690000000,1690000000,0,0,0,1234),
(105,1,1,1,1,1690000000,1690000000,0,0,0,1234),
(106,1,1,1,4,1690000000,1690000000,0,0,0,1234);
create table labels(message_id integer,mailbox_id integer);
insert into labels values (101,2),(102,2),(103,2),(104,2),(105,2);`
	if output, err := exec.Command(binary, path, sql).CombinedOutput(); err != nil {
		t.Fatalf("synthetic DB: %v %s", err, output)
	}
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", path)
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "")
	c := NewClient()
	if err := c.SetBackend(BackendNative); err != nil {
		t.Fatal(err)
	}
	c.shared.accounts = []Account{{ID: "ABC", Name: "Work"}, {ID: "OTHER", Name: "Other"}}
	c.shared.accountsLoaded = true
	events := &atomic.Int64{}
	c.shared.nativeSender = func(context.Context, appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		events.Add(1)
		return appleevents.Value{}, appleevents.Stats{}, errors.New("fixture must not ask Mail for fully cached content")
	}
	message := "Message-ID: <unique@example.com>\nSubject: =?UTF-8?B?5L2g5aW9IGNhZsOp?=\nTo: =?UTF-8?B?5p2O6Zu3?= <li@example.com>, Café <cafe@example.com>\nTo: second@example.com\nCc: \"Unicode 😀\" <cc@example.com>\nContent-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain; charset=utf-8\n\nplain alternative\n--b\nContent-Type: text/html; charset=utf-8\n\n<p>héllo 😀</p><img src=\"https://invalid.example/image\" alt=\"picture\">\n--b--\n"
	owner := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages")
	writeEMLX(t, filepath.Join(owner, "101.emlx"), message)
	writeEMLX(t, filepath.Join(owner, "103.partial.emlx"), "To: partial@example.com\n\npartial body")
	if err := os.WriteFile(filepath.Join(owner, "104.emlx"), []byte("500\nTo: broken@example.com\n\ntruncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeEMLX(t, filepath.Join(root, "ABC", "邮件 café.mbox", "UUID", "Data", "Messages", "106.emlx"), message)
	t.Cleanup(mailindex.Reset)
	return c, root, events
}

func TestLocalReadCompleteMetadataAndPhysicalOwnership(t *testing.T) {
	c, _, events := localReadFixture(t)
	message, handled, err := c.getMessageDetailsFromDisk("Work", "INBOX", "101")
	if err != nil || !handled || message == nil {
		t.Fatalf("details = %#v, %v, %v", message, handled, err)
	}
	if message.RFCMessageID != "<unique@example.com>" || message.Subject != "你好 café" || message.Content != "héllo 😀" || message.ContentSource != "disk" || message.Read || !message.Flagged || message.MessageSize != 1234 || message.Snippet != "indexed summary" || message.Mailbox != "INBOX" || message.Account != "Work" || message.DateReceived == "" {
		t.Fatalf("metadata = %#v", message)
	}
	if strings.Join(message.ToRecipients, ",") != "li@example.com,cafe@example.com,second@example.com" || strings.Join(message.CcRecipients, ",") != "cc@example.com" || len(message.BccRecipients) != 0 {
		t.Fatalf("recipients = %#v", message)
	}
	if message.BodyDiagnostics == nil || message.BodyDiagnostics.PartialFile || message.BodyDiagnostics.HTMLImageCount != 1 || len(message.BodyDiagnostics.Alternatives) != 2 || message.BodyDiagnostics.Alternatives[0].Text != "" {
		t.Fatalf("diagnostics = %#v", message.BodyDiagnostics)
	}
	if events.Load() != 0 {
		t.Fatalf("complete disk detail sent %d Mail events", events.Load())
	}
	var unread []struct{ Read int }
	if err := c.runEnvelopeIndexQuery("select read as Read from messages where ROWID=101;", &unread); err != nil || len(unread) != 1 || unread[0].Read != 0 {
		t.Fatalf("read changed unread state: %v %v", unread, err)
	}
	c.SetBodyAlternatives(true)
	message, handled, err = c.getMessageDetailsFromDisk("Work", "INBOX", "101")
	if err != nil || !handled || message.BodyDiagnostics.Alternatives[0].Text != "plain alternative" {
		t.Fatalf("alternatives = %#v, %v", message, err)
	}
}

func TestLocalReadScopePartialAndUnicode(t *testing.T) {
	c, _, events := localReadFixture(t)
	for _, test := range []struct {
		mailbox, id string
		missing     bool
	}{{"INBOX", "102", true}, {"邮件 café", "101", true}, {"INBOX", "999", true}} {
		message, handled, err := c.getMessageDetailsFromDisk("Work", test.mailbox, test.id)
		if err != nil || !handled || message != nil {
			t.Fatalf("scope %s/%s: %#v %v %v", test.mailbox, test.id, message, handled, err)
		}
	}
	for _, id := range []string{"103", "104", "105"} {
		message, handled, err := c.getMessageDetailsFromDisk("Work", "INBOX", id)
		if err != nil || handled || message == nil {
			t.Fatalf("incomplete %s: %#v %v %v", id, message, handled, err)
		}
		if id == "103" && (message.BodyDiagnostics == nil || !message.BodyDiagnostics.PartialFile || message.ContentError == "") {
			t.Fatalf("partial provenance = %#v", message)
		}
		if id == "104" && (message.BodyDiagnostics == nil || message.BodyDiagnostics.ExtractionError == "") {
			t.Fatalf("parser provenance = %#v", message)
		}
	}
	message, handled, err := c.getMessageDetailsFromDisk("Work", "邮件 café", "106")
	if err != nil || !handled || message == nil || message.Mailbox != "邮件 café" {
		t.Fatalf("Unicode mailbox: %#v %v %v", message, handled, err)
	}
	if events.Load() != 0 {
		t.Fatal("disk helper rendered content")
	}
	if err := c.SetBodySource(BodySourceMail); err != nil {
		t.Fatal(err)
	}
	if message, handled, err := c.getMessageDetailsFromDisk("Work", "INBOX", "101"); err != nil || handled || message != nil {
		t.Fatalf("forced Mail path = %#v %v %v", message, handled, err)
	}
}

func TestLocalBatchBodyExtractionUsesOneOwnershipQuery(t *testing.T) {
	c, _, events := localReadFixture(t)
	messages := make([]Message, 24)
	for i := range messages {
		messages[i] = Message{ID: "101", Account: "Work", Mailbox: "INBOX"}
	}
	before := mailindex.Statistics()
	messages = c.enrichMessagesWithContent("Work", "INBOX", messages)
	after := mailindex.Statistics()
	// One mailbox resolution and one physical-ownership query cover all rows.
	if after.Queries-before.Queries != 2 {
		t.Fatalf("batch used %d queries", after.Queries-before.Queries)
	}
	for _, message := range messages {
		if message.Content != "héllo 😀" || message.ContentSource != "disk" || message.BodyDiagnostics == nil || message.ContentError != "" || message.RFCMessageID != "<unique@example.com>" || len(message.ToRecipients) != 3 {
			t.Fatalf("enriched = %#v", message)
		}
	}
	if events.Load() != 0 {
		t.Fatalf("cached batch sent %d Mail events", events.Load())
	}
	// A canceled local read returns promptly and cannot start a content event.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	c.WithContext(ctx).enrichMessagesWithContent("Work", "INBOX", messages)
	if events.Load() != 0 || time.Since(started) > time.Second {
		t.Fatal("canceled enrichment tried rendering")
	}
}

func TestDiscoverEnvelopeIndexVersionsAndOverride(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"V9", "V10", "V12", "V99x"} {
		path := filepath.Join(root, version, "MailData", "Envelope Index")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic path marker"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Higher version without an index is not usable.
	if err := os.MkdirAll(filepath.Join(root, "V13", "MailData"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := discoverEnvelopeIndexPath(root)
	if err != nil || path != filepath.Join(root, "V12", "MailData", "Envelope Index") {
		t.Fatalf("discovery=%q %v", path, err)
	}
	empty := t.TempDir()
	path, err = discoverEnvelopeIndexPath(empty)
	if err != nil || path != filepath.Join(empty, "V10", "MailData", "Envelope Index") {
		t.Fatalf("fallback=%q %v", path, err)
	}
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", "relative/index")
	if _, err := mailEnvelopeIndexPath(); err == nil {
		t.Fatal("accepted relative override")
	}
}

func TestLocalNativeSchemaAndAccessErrorsDoNotFallback(t *testing.T) {
	c, _, events := localReadFixture(t)
	var rows []struct{ Value string }
	err := c.runEnvelopeIndexQuery("select unsupported_column as Value from messages;", &rows)
	if err == nil || isEnvelopeIndexUnavailable(err) {
		t.Fatalf("schema error would fall back: %v", err)
	}
	denied := c.nativeDiskAccessError(&os.PathError{Op: "open", Path: "synthetic fixture", Err: os.ErrPermission})
	if denied == nil || isEnvelopeIndexUnavailable(denied) {
		t.Fatalf("access denial would fall back: %v", denied)
	}
	if events.Load() != 0 {
		t.Fatal("schema error invoked Mail")
	}
}

func TestLocalContentFallbackGroupsExactAccountAndMailbox(t *testing.T) {
	c, _, _ := localReadFixture(t)
	if err := c.SetBackend(BackendLegacy); err != nil {
		t.Fatal(err)
	}
	if err := c.SetBodySource(BodySourceMail); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "scripts")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$MAIL_APP_CLI_OSA_LOG"
case "$*" in
 *"accounts.byName('Work')"*) printf '%s\n' '{"77":"work first","78":"work second"}' ;;
 *"accounts.byName('Other')"*) printf '%s\n' '{"79":"other body"}' ;;
 *) printf '%s\n' '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_OSA_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	messages := []Message{{ID: "77", Account: "Work", Mailbox: "INBOX"}, {ID: "79", Account: "Other", Mailbox: "INBOX"}, {ID: "78", Account: "Work", Mailbox: "INBOX"}}
	messages = c.enrichMessagesWithContent("", "", messages)
	for i, want := range []string{"work first", "other body", "work second"} {
		if messages[i].Content != want || messages[i].ContentSource != "mail" || messages[i].ContentError != "" {
			t.Fatalf("grouped content[%d]=%#v", i, messages[i])
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "const requestedMailbox") != 2 || strings.Count(string(data), "accounts.byName('Work')") != 1 || strings.Count(string(data), "accounts.byName('Other')") != 1 {
		t.Fatalf("fallback scopes were mixed or repeated: %s", data)
	}
}
