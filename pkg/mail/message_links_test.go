package mail

import (
	"errors"
	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageLinksHTML(t *testing.T) {
	source := `<html><head><base href="https://example.test/news/"></head><body><a href="../leave?a=1&amp;b=two%2Bthree"><span>Unsubscribe</span> now</a><a href="https://example.test/prefs"><img alt="Manage preferences" src="x"></a><a href="javascript:alert(1)">Danger</a><a href="/relative">Relative</a><div hidden><a href="https://hidden.test">hidden</a></div></body></html>`
	links := htmlMessageLinks(source, "1.2")
	if len(links) != 4 {
		t.Fatalf("links: %+v", links)
	}
	if links[0].URL != "https://example.test/leave?a=1&b=two%2Bthree" || links[0].Text != "Unsubscribe now" || links[0].Kind != "unsubscribe" || !links[0].Openable || links[0].MIMEPart != "1.2" {
		t.Fatalf("first: %+v", links[0])
	}
	if links[1].Text != "Manage preferences" || links[1].Kind != "preferences" {
		t.Fatalf("image: %+v", links[1])
	}
	if links[2].Openable || links[2].URL != "javascript:alert(1)" {
		t.Fatalf("unsafe: %+v", links[2])
	}
	if links[3].URL != "https://example.test/relative" {
		t.Fatalf("base: %+v", links[3])
	}
	relative := htmlMessageLinks(`<a href="/leave">Unsubscribe</a>`, "1")
	if relative[0].Openable || relative[0].URL != "/leave" {
		t.Fatalf("relative: %+v", relative)
	}
}
func TestMessageLinksPlainContextAndPunctuation(t *testing.T) {
	links := plainMessageLinks("To unsubscribe, visit the following link:\nhttps://opaque.test/token\nRead [docs](https://example.test/a_(b)).\nMail mailto:leave@example.test?subject=unsubscribe", "1")
	if len(links) != 3 || links[0].Kind != "unsubscribe" || links[1].URL != "https://example.test/a_(b)" || links[1].Kind != "link" || !links[2].Openable {
		t.Fatalf("links: %+v", links)
	}
}
func TestMessageLinksMIMEAlternativesAndEncoding(t *testing.T) {
	source := "Content-Type: multipart/alternative; boundary=outer\n\n--outer\nContent-Type: multipart/alternative; boundary=inner\n\n--inner\nContent-Type: text/plain\n\nhttps://plain.test\n--inner\nContent-Type: text/html\n\n<a href=\"https://nested.test\">nested</a>\n--inner--\n--outer\nContent-Type: text/html; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable\n\n<a href=3D\"https://selected.test/?a=3D1&amp;b=3D2\">caf=E9</a>\n--outer--\n"
	path := filepath.Join(t.TempDir(), "1.emlx")
	writeEMLX(t, path, source)
	body, d, err := readEMLXBodyWithDiagnostics(path)
	if err != nil || body != "café" || len(d.links) != 3 {
		t.Fatalf("body=%q diagnostics=%+v err=%v", body, d, err)
	}
	for i, link := range d.links {
		if link.Selected != (i == 2) {
			t.Fatalf("selection: %+v", d.links)
		}
	}
	if d.links[2].URL != "https://selected.test/?a=1&b=2" || d.links[2].Text != "café" {
		t.Fatalf("decoded: %+v", d.links[2])
	}
}
func TestMessageLinksDoNotHarvestAttachments(t *testing.T) {
	source := "Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nhttps://body.test\n--b\nContent-Type: text/html\nContent-Disposition: attachment; filename=links.html\n\n<a href=\"https://attachment.test\">Unsubscribe</a>\n--b--\n"
	path := filepath.Join(t.TempDir(), "1.emlx")
	writeEMLX(t, path, source)
	_, d, err := readEMLXBodyWithDiagnostics(path)
	if err != nil || len(d.links) != 1 || d.links[0].URL != "https://body.test" {
		t.Fatalf("links: %+v %v", d, err)
	}
}
func TestMessageUnsubscribeHeaders(t *testing.T) {
	raw := "List-Unsubscribe: <mailto:leave@example.test?subject=stop>,\r\n <https://example.test/leave?a=1&b=2>\r\nList-Unsubscribe: <javascript:bad>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\nSubject: test\r\n\r\n"
	rfc, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{}
	populateUnsubscribe(m, rfc.Header)
	populateUnsubscribe(m, rfc.Header)
	if len(m.Links) != 3 || len(m.Unsubscribe.ListUnsubscribe) != 2 || !m.Unsubscribe.OneClickAdvertised || m.Unsubscribe.HeaderStatus != "available" {
		t.Fatalf("message: %+v %+v", m, m.Unsubscribe)
	}
	if m.Links[1].URL != "https://example.test/leave?a=1&b=2" || m.Links[2].Openable {
		t.Fatalf("links: %+v", m.Links)
	}
	for _, l := range m.Links {
		if l.Source != "header" || l.Kind != "unsubscribe" {
			t.Fatalf("link: %+v", l)
		}
	}
	populateUnsubscribe(m, mail.Header{})
	if m.Unsubscribe.OneClickAdvertised || m.Unsubscribe.HeaderStatus != "available" || len(m.Links) != 0 {
		t.Fatalf("empty headers: %+v", m)
	}
}
func TestMessageLinksPartialHydrationPreservesDiscoveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "1.partial.emlx")
	writeEMLX(t, path, "List-Unsubscribe: <https://headers.test/leave>\nContent-Type: text/html\n\n<a href=\"https://body.test/leave\">Unsubscribe</a>")
	c := NewClient()
	m := &Message{}
	complete, err := c.hydrateLocalMessage(m, path)
	if err != nil || complete {
		t.Fatalf("hydrate complete=%v err=%v", complete, err)
	}
	if m.LinkStatus == nil || m.LinkStatus.Complete || len(m.Links) != 2 || len(m.LinkStatus.Warnings) == 0 {
		t.Fatalf("partial: %+v", m)
	}
	m.Content = "Mail fallback https://fallback.test"
	populateMailLinks(m)
	if len(m.Links) != 2 || m.Links[1].URL != "https://body.test/leave" {
		t.Fatalf("fallback discarded discoveries: %+v", m)
	}
}

func TestMessageLinksDedicatedPartialLocalAvoidsMail(t *testing.T) {
	c, root, events := localReadFixture(t)
	path := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages", "103.partial.emlx")
	writeEMLX(t, path, "List-Unsubscribe: <https://headers.test/leave>\nContent-Type: text/html\n\n<a href=\"https://body.test/leave\">Unsubscribe</a>")
	if err := c.SetBodySource(BodySourceMail); err != nil {
		t.Fatal(err)
	}
	m, err := c.GetMessageLinksJSON("Work", "INBOX", "103")
	if err != nil || m == nil || len(m.Links) != 2 || m.LinkStatus.Complete || events.Load() != 0 {
		t.Fatalf("m=%+v err=%v events=%d", m, err, events.Load())
	}
}
func TestMessageLinksDedicatedNativeHeadersAndScope(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	f.properties["mssg:42"]["alhe"] = appleevents.String("List-Unsubscribe: <https://example.test/leave>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click")
	m, err := c.GetMessageLinksJSON("Personal", "INBOX", "42")
	if err != nil || m == nil || len(m.Links) != 1 || !m.Unsubscribe.OneClickAdvertised || m.LinkStatus.Complete {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	assertNoNativeContentRead(t, f.events)
	// A global by-ID getter still succeeds, while the scoped collection excludes it.
	// Derive the actual mailbox key used by this synthetic fixture.
	for key := range f.messages {
		f.messages[key] = nil
	}
	_, err = c.GetMessageLinksJSON("Personal", "INBOX", "42")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope err=%v", err)
	}
}
func TestMessageLinksDedicatedNativePermissionIsError(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	denied := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.fail = func(event appleevents.Event) error {
		if event.Params["----"].Fields["seld"].Code == "alhe" {
			return denied
		}
		return nil
	}
	_, err := c.GetMessageLinksJSON("Personal", "INBOX", "42")
	if !errors.Is(err, denied) {
		t.Fatalf("permission err=%v", err)
	}
	assertNoNativeContentRead(t, f.events)
}
func TestMessageLinksDedicatedLegacyHeadersAvoidRendering(t *testing.T) {
	fakeMailStore(t, recordScript+`printf '%s\n' '{"id":"77","headers":"List-Unsubscribe: <https://example.test/leave>"}'
`)
	c := NewClient()
	c.shared.accounts = []Account{{ID: "ABC", Name: "Work"}}
	c.shared.accountsLoaded = true
	if err := c.SetBackend(BackendLegacy); err != nil {
		t.Fatal(err)
	}
	m, err := c.GetMessageLinksJSON("Work", "Spam", "77")
	if err != nil || m == nil || len(m.Links) != 1 || m.LinkStatus.Complete {
		t.Fatalf("m=%+v err=%v", m, err)
	}
	script, err := os.ReadFile(os.Getenv("MAIL_APP_CLI_OSA_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), ".content()") || !strings.Contains(string(script), "mbox.messages.id()") {
		t.Fatalf("script scope/body: %s", script)
	}
}

func TestMessageLinksGenericAnchorContextIsLocal(t *testing.T) {
	source := `<article><p>Update your email preferences or unsubscribe <a href="https://opaque.test/token">here</a></p><p>Read more <a href="https://opaque.test/docs">here</a></p><p>Documentation <a href="https://opaque.test/manual">here</a>, or unsubscribe <a href="https://opaque.test/leave">here</a>. <a href="https://opaque.test/other">Unrelated docs</a></p></article>`
	links := htmlMessageLinks(source, "1")
	if len(links) != 5 {
		t.Fatalf("links=%+v", links)
	}
	for i, kind := range []string{"unsubscribe", "link", "link", "unsubscribe", "link"} {
		if links[i].Kind != kind {
			t.Fatalf("link %d=%+v want kind %s", i, links[i], kind)
		}
	}
	if links[0].Text != "here" {
		t.Fatalf("changed label: %+v", links[0])
	}
}
func TestMessageLinksDelimitedPlainPreservesExactDestination(t *testing.T) {
	for _, token := range []string{"abc;", "abc.", "abc,"} {
		destination := "https://example.test/leave?token=" + token
		for _, text := range []string{"<" + destination + ">", "[unsubscribe](" + destination + ")", "\"" + destination + "\""} {
			links := plainMessageLinks(text, "1")
			if len(links) != 1 || links[0].URL != destination {
				t.Fatalf("text=%q links=%+v", text, links)
			}
		}
	}
}
func TestMessageLinksEnvelopeFailureStillExtractsBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "1.emlx")
	writeEMLX(t, path, "Subject: =?bad-charset?Q?broken?=\nList-Unsubscribe: <https://headers.test/leave>\nContent-Type: text/html\n\n<p>Unsubscribe <a href=\"https://opaque.test/token\">here</a></p>")
	c := NewClient()
	m := &Message{}
	complete, err := c.hydrateLocalMessage(m, path)
	if err != nil || complete || m.Content != "" || len(m.Links) != 2 || m.LinkStatus == nil || len(m.LinkStatus.Warnings) == 0 {
		t.Fatalf("m=%+v complete=%v err=%v", m, complete, err)
	}
	if m.Links[1].Kind != "unsubscribe" || !strings.Contains(m.ContentError, "Subject") {
		t.Fatalf("m=%+v", m)
	}
}

func TestMessageLinksSelectRenderableAlternativeWithoutChangingText(t *testing.T) {
	plain := "Content-Type: text/plain\n\nhttps://plain.test/body\n"
	imageHTML := "Content-Type: text/html\n\n<a href=\"https://html.test/leave\"><img src=\"cid:button\"></a>\n"
	richHTML := "Content-Type: text/html\n\n<a href=\"https://html.test/leave\">HTML rendering</a>\n"
	tests := []struct {
		name        string
		body        string
		wantContent string
		selectedURL string
		wantLinks   int
	}{
		{"image-only HTML", "Content-Type: multipart/alternative; boundary=a\n\n--a\n" + plain + "--a\n" + imageHTML + "--a--\n", "https://plain.test/body", "https://html.test/leave", 2},
		{"HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: text/html\n\n<a href=\"https://html.test/leave\">HTML rendering</a>\n--a\n" + plain + "--a--\n", "HTML rendering", "https://html.test/leave", 2},
		{"image-only HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\n" + imageHTML + "--a\n" + plain + "--a--\n", "https://plain.test/body", "https://html.test/leave", 2},
		{"related HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/related; boundary=r\n\n--r\n" + richHTML + "--r--\n--a\n" + plain + "--a--\n", "HTML rendering", "https://html.test/leave", 2},
		{"nested HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/alternative; boundary=b\n\n--b\n" + richHTML + "--b--\n--a\n" + plain + "--a--\n", "HTML rendering", "https://html.test/leave", 2},
		{"mixed HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/mixed; boundary=b\n\n--b\n" + richHTML + "--b--\n--a\n" + plain + "--a--\n", "HTML rendering", "https://html.test/leave", 2},
		{"related declared HTML root before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/related; boundary=r; start=\"<root>\"\n\n--r\nContent-Type: image/png\n\nimage\n--r\nContent-ID: <root>\n" + richHTML + "--r--\n--a\n" + plain + "--a--\n", "HTML rendering", "https://html.test/leave", 2},
		{"related image-only HTML before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/related; boundary=r\n\n--r\n" + imageHTML + "--r--\n--a\n" + plain + "--a--\n", "https://plain.test/body", "https://html.test/leave", 2},
		{"plain composite before plain", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\nhttps://earlier.test/body\n--b--\n--a\n" + plain + "--a--\n", "https://plain.test/body", "https://plain.test/body", 2},
		{"related auxiliary HTML does not make root rich", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/related; boundary=r\n\n--r\nContent-Type: text/plain\n\nhttps://earlier.test/body\n--r\n" + richHTML + "--r--\n--a\n" + plain + "--a--\n", "https://plain.test/body", "https://plain.test/body", 3},
		{"related auxiliary links remain unselected", "Content-Type: multipart/related; boundary=r\n\n--r\n" + plain + "--r\n" + richHTML + "--r--\n", "https://plain.test/body\nHTML rendering", "https://plain.test/body", 2},
		{"related root selected by Content-ID", "Content-Type: multipart/alternative; boundary=a\n\n--a\n" + plain + "--a\nContent-Type: multipart/related; boundary=r; start=\"<root>\"\n\n--r\nContent-Type: image/png\nContent-ID: <button>\n\nimage\n--r\nContent-ID: <root>\n" + imageHTML + "--r--\n--a--\n", "https://plain.test/body", "https://html.test/leave", 2},
		{"nested alternatives", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: text/plain\n\nhttps://outer.test/body\n--a\nContent-Type: multipart/alternative; boundary=b\n\n--b\n" + plain + "--b\n" + imageHTML + "--b--\n--a--\n", "https://plain.test/body", "https://html.test/leave", 3},
		{"discarded nested rendering remains unselected", "Content-Type: multipart/alternative; boundary=a\n\n--a\nContent-Type: multipart/alternative; boundary=b\n\n--b\n" + plain + "--b\n" + imageHTML + "--b--\n--a\nContent-Type: text/html\n\n<a href=\"https://final.test/body\">Final rendering</a>\n--a--\n", "Final rendering", "https://final.test/body", 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "1.emlx")
			writeEMLX(t, path, test.body)
			content, d, err := readEMLXBodyWithDiagnostics(path)
			if err != nil || content != test.wantContent || len(d.links) != test.wantLinks {
				t.Fatalf("content=%q links=%+v err=%v", content, d.links, err)
			}
			for _, link := range d.links {
				if link.Selected != (link.URL == test.selectedURL) {
					t.Fatalf("selection=%+v expected selected URL %s", d.links, test.selectedURL)
				}
			}
			if test.name == "image-only HTML" {
				// Legacy diagnostics continue describing the nonempty text projection.
				if len(d.Alternatives) != 2 || !d.Alternatives[0].Selected || d.Alternatives[1].Selected {
					t.Fatalf("legacy diagnostics changed: %+v", d.Alternatives)
				}
			}
		})
	}
}

func TestMessageLinksCompletenessRequiresSupportedBody(t *testing.T) {
	for _, test := range []struct {
		name, contentType, body string
		complete                bool
		linkCount               int
	}{
		{"encrypted-only MIME", "application/pkcs7-mime; smime-type=enveloped-data", "opaque encrypted payload", false, 1},
		{"image-only HTML", "text/html", `<a href="https://body.test/leave"><img src="cid:button"></a>`, true, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "1.emlx")
			writeEMLX(t, path, "List-Unsubscribe: <https://headers.test/leave>\nContent-Type: "+test.contentType+"\n\n"+test.body)
			rfc, err := readEMLXMessage(path)
			if err != nil {
				t.Fatal(err)
			}
			message := &Message{}
			populateUnsubscribe(message, rfc.Header)
			content, diagnostics, extractionErr := extractEMLXBody(rfc, path)
			if content != "" || extractionErr == nil || extractionErr.Error() != "message file holds no text body" {
				t.Fatalf("unexpected text projection content=%q err=%v", content, extractionErr)
			}
			populateDiskLinks(message, diagnostics, extractionErr)
			if message.LinkStatus == nil || message.LinkStatus.Complete != test.complete || len(message.Links) != test.linkCount {
				t.Fatalf("status=%+v links=%+v", message.LinkStatus, message.Links)
			}
			if message.Links[0].URL != "https://headers.test/leave" || message.Links[0].Source != "header" || message.Unsubscribe.HeaderStatus != "available" {
				t.Fatalf("header control lost: %+v", message)
			}
			if test.complete && len(message.LinkStatus.Warnings) != 0 {
				t.Fatalf("image-only HTML incorrectly warned incomplete: %+v", message.LinkStatus)
			}
			if !test.complete && len(message.LinkStatus.Warnings) != 1 {
				t.Fatalf("unsupported MIME should retain one extraction warning: %+v", message.LinkStatus)
			}
		})
	}
}
