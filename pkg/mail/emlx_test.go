package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEMLX(t *testing.T, path, message string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	message = strings.ReplaceAll(message, "\n", "\r\n")
	data := fmt.Sprintf("%d\n%s<?xml version=\"1.0\"?><plist><dict/></plist>\n", len(message), message)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMessageDataDirectory(t *testing.T) {
	for id, want := range map[int64]string{7: "Data", 999: "Data", 1000: "Data/1", 256493: "Data/6/5/2", 1204005: "Data/4/0/2/1"} {
		if got := messageDataDirectory(id); got != filepath.FromSlash(want) {
			t.Fatalf("messageDataDirectory(%d) = %q, want %q", id, got, want)
		}
	}
}

func TestMailboxDirectory(t *testing.T) {
	got, err := mailboxDirectory("/root", "imap://ABC/%5BGmail%5D/All%20Mail")
	if err != nil || got != filepath.FromSlash("/root/ABC/[Gmail].mbox/All Mail.mbox") {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"imap://ABC/..", "imap://ABC/a%2Fb", "not a url", "imap:///INBOX"} {
		if dir, err := mailboxDirectory("/root", bad); err == nil {
			t.Fatalf("mailboxDirectory(%q) = %q, want an error", bad, dir)
		}
	}
}

func TestReadEMLXBody(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
		wantErr string
	}{
		{"plain", "Subject: s\nContent-Type: text/plain; charset=utf-8\n\nline one\nline two\n", "line one\nline two", ""},
		{"reply markers", "Content-Type: text/plain\n\nOn Monday someone wrote:\n> first\n> > second\n>\n>third\n2 > 1\n", "On Monday someone wrote:\nfirst\nsecond\n\nthird\n2 > 1", ""},
		{"no content type", "Subject: s\n\nbare body\n", "bare body", ""},
		{"quoted printable latin1", "Content-Type: text/plain; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable\n\ncaf=E9 so=\nft\n", "café soft", ""},
		{"base64", "Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\naGVsbG8g\nd29ybGQ=\n", "hello world", ""},
		{"alternative shows html", "Content-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\nplain version https://example.com/long\n--b\nContent-Type: text/html\n\n<html><head><title>t</title><style>p{}</style></head><body><p>rich <a href=\"x\">version</a>.</p></body></html>\n--b--\n", "rich version.", ""},
		{"alternative without html", "Content-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\nonly plain\n--b--\n", "only plain", ""},
		{"attachment skipped", "Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nbody\n--b\nContent-Type: text/plain\nContent-Disposition: attachment; filename=a.txt\n\nattached\n--b\nContent-Type: application/pdf\nContent-Transfer-Encoding: base64\n\nJVBERg==\n--b--\n", "body", ""},
		{"nested", "Content-Type: multipart/mixed; boundary=outer\n\n--outer\nContent-Type: multipart/alternative; boundary=inner\n\n--inner\nContent-Type: text/plain\n\nplain\n--inner\nContent-Type: text/html\nContent-Transfer-Encoding: quoted-printable\n\n<div>ni=C3=B1o</div>\n--inner--\n--outer--\n", "niño", ""},
		{"unknown charset", "Content-Type: text/plain; charset=x-no-such-charset\n\nbody\n", "", "unsupported charset"},
		{"no text", "Content-Type: application/pdf\n\nJVBERg==\n", "", "no text body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "1.emlx")
			writeEMLX(t, path, test.message)
			got, err := readEMLXBody(path)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("got %q, %v; want error %q", got, err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestReadEMLXBodyRejectsDamagedFile(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"nolength": "Subject: s\n\nbody", "short": "500\nSubject: s\n\nbody", "empty": ""} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if body, err := readEMLXBody(path); err == nil {
			t.Fatalf("%s: got %q, want an error", name, body)
		}
	}
}

func TestHTMLToTextMatchesMailRendering(t *testing.T) {
	for name, test := range map[string]struct{ html, want string }{
		"inline elements add no space": {`<p>See <a href="x">the docs</a>, then <b>go</b>.</p>`, "See the docs, then go."},
		"source whitespace collapses":  {"<p>one\n      two</p>\n\n\n<p>three</p>", "one two\n\nthree"},
		"display none is dropped":      {`<div style="display: none !important">preview</div><p>body</p>`, "body"},
		"invisible text is kept":       {`<div style="visibility:hidden;max-height:0;overflow:hidden;opacity:0">preview</div><p>body</p>`, "preview\n\nbody"},
		"hidden subtree closes":        {`<div style="display:none"><p>a<br>b</p><img src="x"></div><p>shown</p>`, "shown"},
		"lists":                        {`<ul><li>a</li><li>b<ul><li>c</li></ul></li></ul><ol><li>x</li><li>y</li></ol><ul style="list-style-type:circle"><li>z</li></ul>`, "• a\n\n• b\n\n◦ c\n\n1. x\n\n2. y\n\n◦ z"},
		"table cells are separated":    {`<table><tr><td>T:</td><td>+1 555</td></tr></table>`, "T: +1 555"},
		"non-breaking space":           {"<span>curl</span><span>&nbsp;-s</span>", "curl -s"},
		"text transform":               {`<p style="text-transform: uppercase">Sep <b>28</b></p><p>Sep</p>`, "SEP 28\n\nSep"},
		"zero-width space":             {"<p>2\u200b447</p>", "2 447"},
		"entities":                     {`<p>a &amp; b &lt;c&gt;</p>`, "a & b <c>"},
		"unclosed and stray tags":      {`</div><p>one<p>two</span>`, "one\ntwo"},
	} {
		if got := htmlToText(test.html); got != test.want {
			t.Fatalf("%s: got %q, want %q", name, got, test.want)
		}
	}
}

// fakeMailStore lays out one message file and an index that points at it.
func fakeMailStore(t *testing.T, osascript string) {
	t.Helper()
	home, binDir := t.TempDir(), t.TempDir()
	sqlite := `#!/bin/sh
case "$4" in
  *"mb.url as URL"*"m.ROWID = 256493"*) printf '%s\n' '[{"URL":"imap://ABC/%5BGmail%5D/Spam"}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(sqlite), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\n"+osascript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEMLX(t, filepath.Join(home, "Library/Mail/V10/ABC/[Gmail].mbox/Spam.mbox/UUID/Data/6/5/2/Messages/256493.emlx"), "Content-Type: text/plain\n\nbody from disk\n")
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	t.Setenv("MAIL_APP_CLI_OSA_LOG", filepath.Join(t.TempDir(), "osa.log"))
}

const recordScript = `printf '%s\n' "$*" >> "$MAIL_APP_CLI_OSA_LOG"
`

func TestDetailReadTakesBodyFromDiskWithoutAskingMail(t *testing.T) {
	fakeMailStore(t, recordScript+`printf '%s\n' '{"message":{"id":"256493","subject":"s","content":""},"failure":null,"phases":[{"name":"metadata","elapsedMs":4}]}'
`)
	message, trace, err := NewClient().getMessageDetailsTraced("Work", "Spam", "256493", 5*time.Second, true)
	if err != nil || message == nil || message.Content != "body from disk" || message.ContentSource != "disk" {
		t.Fatalf("message = %+v, %v", message, err)
	}
	if len(trace.Phases) != 2 || trace.Phases[0].Name != "disk_body" || trace.Phases[0].Note != "" || trace.LastCompleted != "metadata" {
		t.Fatalf("trace = %+v", trace)
	}
	script, err := os.ReadFile(os.Getenv("MAIL_APP_CLI_OSA_LOG"))
	if err != nil || !strings.Contains(string(script), "const skipContent = true;") {
		t.Fatalf("Mail.app was still asked for the body: %v", err)
	}
}

func TestDetailReadFallsBackToMailWithoutMessageFile(t *testing.T) {
	fakeMailStore(t, recordScript+`printf '%s\n' '{"message":{"id":"77","content":"body from mail"},"failure":null,"phases":[{"name":"content","elapsedMs":9}]}'
`)
	message, trace, err := NewClient().getMessageDetailsTraced("Work", "Spam", "77", 5*time.Second, true)
	if err != nil || message == nil || message.Content != "body from mail" || message.ContentSource != "mail" {
		t.Fatalf("message = %+v, %v", message, err)
	}
	if trace.Phases[0].Name != "disk_body" || !strings.Contains(trace.Phases[0].Note, "unavailable") {
		t.Fatalf("trace = %+v", trace)
	}
	script, _ := os.ReadFile(os.Getenv("MAIL_APP_CLI_OSA_LOG"))
	if !strings.Contains(string(script), "const skipContent = false;") {
		t.Fatal("Mail.app was not asked for the body")
	}
}

func TestDetailReadWithoutDiskPreferenceAlwaysAsksMail(t *testing.T) {
	fakeMailStore(t, recordScript+`printf '%s\n' '{"message":{"id":"256493","content":"body from mail"},"failure":null,"phases":[]}'
`)
	message, err := NewClient().getMessageDetailsFromMail("Work", "Spam", "256493")
	if err != nil || message.Content != "body from mail" || message.ContentSource != "mail" {
		t.Fatalf("message = %+v, %v", message, err)
	}
}

func TestDetailReadTimeoutKeepsDiskPhase(t *testing.T) {
	fakeMailStore(t, `printf 'mail-app-cli-phase {"name":"resolve_account","state":"start","atMs":1}\n' >&2
/bin/sleep 30
`)
	_, trace, err := NewClient().getMessageDetailsTraced("Work", "Spam", "256493", 2*time.Second, true)
	if err == nil || trace == nil || trace.Phases[0].Name != "disk_body" || trace.Pending != "resolve_account" || trace.LastCompleted != "disk_body" {
		t.Fatalf("trace = %+v, %v", trace, err)
	}
}
