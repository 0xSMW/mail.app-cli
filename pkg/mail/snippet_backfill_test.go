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

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
)

// snippetFixture builds a client over a synthetic Envelope Index whose INBOX
// holds messages with and without indexed summaries, plus the message files
// Mail would store for them. The automation stub fails every Apple Event, so
// any attempt to ask Mail for a listing detail surfaces as a test failure.
func snippetFixture(t *testing.T) (*Client, string, *atomic.Int64) {
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
(1,'imap://ABC/INBOX',8,8),(3,'imap://OTHER/INBOX',1,1);
create table subjects(subject text); insert into subjects values ('indexed subject');
create table addresses(address text,comment text); insert into addresses values ('sender@example.com','Sender');
create table summaries(summary text); insert into summaries values ('indexed summary');
create table messages(subject integer,sender integer,summary integer,mailbox integer,date_received integer,date_sent integer,read integer,flagged integer,deleted integer,size integer,subject_prefix text default '');
insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values
(201,1,1,1,1,1690000201,1690000201,0,0,0,1234),
(202,1,1,null,1,1690000202,1690000202,0,0,0,1234),
(203,1,1,null,1,1690000203,1690000203,0,0,0,1234),
(204,1,1,null,1,1690000204,1690000204,0,0,0,1234),
(205,1,1,null,1,1690000205,1690000205,0,0,0,1234),
(206,1,1,null,1,1690000206,1690000206,0,0,0,1234),
(207,1,1,null,1,1690000207,1690000207,0,0,0,1234),
(208,1,1,null,1,1690000208,1690000208,0,0,0,1234),
(209,1,1,null,3,1690000209,1690000209,0,0,0,1234);
create table labels(message_id integer,mailbox_id integer);`
	if output, err := exec.Command(binary, path, sql).CombinedOutput(); err != nil {
		t.Fatalf("synthetic DB: %v %s", err, output)
	}
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", path)
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "")
	t.Setenv("MAIL_APP_CLI_DISABLE_SNIPPET_BACKFILL", "")
	c := NewClient()
	if err := c.SetBackend(BackendNative); err != nil {
		t.Fatal(err)
	}
	c.shared.accounts = []Account{{ID: "ABC", Name: "Work"}, {ID: "OTHER", Name: "Other"}}
	c.shared.accountsLoaded = true
	events := &atomic.Int64{}
	c.shared.nativeSender = func(context.Context, appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		events.Add(1)
		return appleevents.Value{}, appleevents.Stats{}, errors.New("fixture must not ask Mail for listings")
	}
	messages := filepath.Join(root, "ABC", "INBOX.mbox", "UUID", "Data", "Messages")
	writeEMLX(t, filepath.Join(messages, "201.emlx"), "From: a@example.com\nContent-Type: text/plain; charset=utf-8\n\ndisk body must not override the indexed summary")
	writeEMLX(t, filepath.Join(messages, "202.emlx"), "From: a@example.com\nContent-Type: text/plain; charset=utf-8\n\nLine one.\nLine two continues here.")
	writeEMLX(t, filepath.Join(messages, "203.emlx"), "From: a@example.com\nContent-Type: text/html; charset=utf-8\n\n<html><body><p>html only body</p></body></html>")
	if err := os.WriteFile(filepath.Join(messages, "204.emlx"), []byte("500\nTo: broken@example.com\n\ntruncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeEMLX(t, filepath.Join(messages, "206.partial.emlx"), "From: a@example.com\nContent-Type: text/plain; charset=utf-8\n\npartial download body")
	writeEMLX(t, filepath.Join(messages, "207.emlx"), "From: a@example.com\nContent-Type: text/plain; charset=utf-8\n\n"+strings.Repeat("word ", 80))
	writeEMLX(t, filepath.Join(messages, "208.emlx"), "From: a@example.com\nContent-Type: text/plain; charset=iso-8859-1\n\nGr\xfc\xdfe aus der Ferne")
	return c, root, events
}

func snippetFixtureMailbox(t *testing.T, c *Client, account, mailbox string) *indexMailbox {
	t.Helper()
	mbox, ok, err := c.resolveIndexMailbox(account, mailbox)
	if err != nil || !ok || mbox == nil {
		t.Fatalf("resolve %s/%s = %v, %v, %v", account, mailbox, mbox, ok, err)
	}
	return mbox
}

func snippetsByID(messages []Message) map[string]string {
	snippets := make(map[string]string, len(messages))
	for _, message := range messages {
		snippets[message.ID] = message.Snippet
	}
	return snippets
}

func TestSnippetBackfillOnIndexListings(t *testing.T) {
	c, _, events := snippetFixture(t)
	mbox := snippetFixtureMailbox(t, c, "Work", "INBOX")
	messages, err := c.getMessagesFromIndex("Work", mbox, 100, 0, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	snippets := snippetsByID(messages)
	for id, want := range map[string]string{
		"201": "indexed summary", // the indexed summary always wins
		"202": "Line one. Line two continues here.",
		"203": "html only body",
		"204": "", // undecodable file leaves the snippet empty
		"205": "", // no file on disk leaves the snippet empty
		"206": "partial download body",
		"207": strings.TrimSuffix(strings.Repeat("word ", 40), " "),
		"208": "Grüße aus der Ferne",
	} {
		if snippets[id] != want {
			t.Fatalf("message %s snippet = %q, want %q", id, snippets[id], want)
		}
	}
	if events.Load() != 0 {
		t.Fatalf("backfill asked Mail %d times, listings must stay index+disk only", events.Load())
	}

	// A mailbox whose message store is absent from disk keeps empty snippets.
	other := snippetFixtureMailbox(t, c, "Other", "INBOX")
	otherMessages, err := c.getMessagesFromIndex("Other", other, 100, 0, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if snippet := snippetsByID(otherMessages)["209"]; snippet != "" {
		t.Fatalf("message without a mailbox directory got snippet %q", snippet)
	}
}

func TestSnippetBackfillCoversSearchAndScan(t *testing.T) {
	c, _, _ := snippetFixture(t)
	mbox := snippetFixtureMailbox(t, c, "Work", "INBOX")

	found, err := c.searchMessagesFromIndex("indexed", "Work", mbox, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if snippet := snippetsByID(found)["202"]; snippet != "Line one. Line two continues here." {
		t.Fatalf("search snippet = %q, want the disk-derived line", snippet)
	}

	page, err := c.scanMessagesFromIndex("Work", mbox, ScanRequest{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snippet := snippetsByID(page.Messages)["202"]; snippet != "Line one. Line two continues here." {
		t.Fatalf("scan snippet = %q, want the disk-derived line", snippet)
	}
}

func TestSnippetBackfillDisabledByEnv(t *testing.T) {
	c, _, _ := snippetFixture(t)
	t.Setenv("MAIL_APP_CLI_DISABLE_SNIPPET_BACKFILL", "1")
	mbox := snippetFixtureMailbox(t, c, "Work", "INBOX")
	messages, err := c.getMessagesFromIndex("Work", mbox, 100, 0, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if snippet := snippetsByID(messages)["202"]; snippet != "" {
		t.Fatalf("disable knob ignored, snippet = %q", snippet)
	}
}

func TestSnippetBackfillDisabledByBodySourceMail(t *testing.T) {
	c, _, _ := snippetFixture(t)
	if err := c.SetBodySource(BodySourceMail); err != nil {
		t.Fatal(err)
	}
	mbox := snippetFixtureMailbox(t, c, "Work", "INBOX")
	messages, err := c.getMessagesFromIndex("Work", mbox, 100, 0, false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if snippet := snippetsByID(messages)["202"]; snippet != "" {
		t.Fatalf("body source mail ignored, snippet = %q", snippet)
	}
}

func TestSnippetFromBody(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"collapses whitespace":    {"  Line one.\n\tLine\ttwo\r\nhere  ", "Line one. Line two here"},
		"empty body":              {"", ""},
		"whitespace only":         {" \n\t \n ", ""},
		"cut at a word boundary":  {strings.Repeat("word ", 80), strings.TrimSuffix(strings.Repeat("word ", 40), " ")},
		"hard cut on one token":   {strings.Repeat("x", 300), strings.Repeat("x", 200)},
		"unicode runes not bytes": {"你好世界" + strings.Repeat("好", 300), "你好世界" + strings.Repeat("好", 196)},
	} {
		if got := snippetFromBody(tc.body); got != tc.want {
			t.Fatalf("%s: snippetFromBody = %q, want %q", name, got, tc.want)
		}
	}
}
