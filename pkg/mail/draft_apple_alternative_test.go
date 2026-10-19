package mail

import (
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

const applePlainWrapperFixture = `<html><head></head><body dir="auto" style="overflow-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;"><div class="Apple-Mail-URLShareUserContentTopClass"><br></div><div class="Apple-Mail-URLShareWrapperClass" style="position: relative !important;"><blockquote type="cite" style="border-left-style: none; color: inherit; padding: inherit; margin: inherit;">

Hello &amp; 世界<br>Second &lt;line&gt;&#160;end<br>

</blockquote></div></body></html>`

func appleAlternativeFixture(t *testing.T) *draftSnapshot {
	t.Helper()
	snapshot := simpleDraftSnapshot(t)
	snapshot.Message.Content = "\nHello & 世界\nSecond <line>\u00a0end\n\n"
	snapshot.Source = "From: alias@example.test\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Draft\r\nContent-Type: multipart/alternative; boundary=apple-alternative\r\n\r\n" + appleAlternativeParts("", applePlainWrapperFixture)
	return snapshot
}

func appleAlternativeParts(plain, wrapper string) string {
	return "--apple-alternative\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" + plain + "\r\n--apple-alternative\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + wrapper + "\r\n--apple-alternative--\r\n"
}

func TestDraftAppleAlternativePreservesExactPlainBody(t *testing.T) {
	for _, content := range []string{
		"Hello & 世界\nSecond <line>\u00a0end", "\nHello & 世界\nSecond <line>\u00a0end\n\n", "\r\nHello & 世界\r\nSecond <line>\u00a0end\r\n",
	} {
		snapshot := appleAlternativeFixture(t)
		snapshot.Message.Content = content
		body, attachments, err := reconstructibleDraft(snapshot)
		if err != nil || body != content || len(attachments) != 0 {
			t.Fatalf("body=%q attachments=%v err=%v; want exact %q", body, attachments, err, content)
		}
	}
}

func TestDraftAppleAlternativeRejectsFormattingOrDifferentContent(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"different text", "Hello &amp; 世界", "Changed body"},
		{"different internal line break", "<br>Second", "Second"},
		{"top class", "Apple-Mail-URLShareUserContentTopClass", "other"},
		{"wrapper class", "Apple-Mail-URLShareWrapperClass", "other"},
		{"wrapper style", "position: relative !important;", "font-weight: bold;"},
		{"quote style", "border-left-style: none;", "border-left-style: solid;"},
		{"body style", "line-break: after-white-space;", "font-size: 20px;"},
		{"body attribute", `dir="auto"`, `dir="auto" lang="en"`},
		{"link", "Hello &amp; 世界", `<a href="https://example.test">Hello &amp; 世界</a>`},
		{"span", "Hello &amp; 世界", `<span>Hello &amp; 世界</span>`},
		{"image", "Hello &amp; 世界", `<img src="cid:picture">Hello &amp; 世界`},
		{"comment", "Hello &amp; 世界", `<!--hidden-->Hello &amp; 世界`},
		{"br attributes", "<br>Second", `<br class="other">Second`},
		{"head content", "<head></head>", "<head><style>body { color: red }</style></head>"},
		{"extra body content", "</blockquote>", "</blockquote>more text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := appleAlternativeFixture(t)
			snapshot.Source = strings.Replace(snapshot.Source, tc.before, tc.after, 1)
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted changed formatting or content")
			}
		})
	}
}

func TestDraftAppleAlternativeRejectsExtraMIMESemantics(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"nonempty plain", "\r\n\r\n\r\n--apple-alternative", "\r\n\r\ncontradictory plain\r\n--apple-alternative"},
		{"plain spaces", "\r\n\r\n\r\n--apple-alternative", "\r\n\r\n \r\n--apple-alternative"},
		{"extra part", "--apple-alternative--", "--apple-alternative\r\nContent-Type: text/plain\r\n\r\nextra\r\n--apple-alternative--"},
		{"inline part header", "Content-Type: text/html; charset=utf-8", "Content-ID: <inline>\r\nContent-Type: text/html; charset=utf-8"},
		{"disposition", "Content-Type: text/html; charset=utf-8", "Content-Disposition: inline\r\nContent-Type: text/html; charset=utf-8"},
		{"duplicate content type", "Content-Type: text/html; charset=utf-8", "Content-Type: text/html; charset=utf-8\r\nContent-Type: text/html; charset=utf-8"},
		{"related", "Content-Type: multipart/alternative;", "Content-Type: multipart/related;"},
		{"signed", "Content-Type: multipart/alternative;", "Content-Type: multipart/signed;"},
		{"encrypted", "Content-Type: multipart/alternative;", "Content-Type: multipart/encrypted;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := appleAlternativeFixture(t)
			snapshot.Source = strings.Replace(snapshot.Source, tc.before, tc.after, 1)
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unsupported MIME semantics")
			}
		})
	}
}

func TestDraftAppleAlternativeMixedPreservesAttachments(t *testing.T) {
	snapshot := appleAlternativeFixture(t)
	headerEnd := strings.Index(snapshot.Source, "Content-Type:")
	snapshot.Source = snapshot.Source[:headerEnd] + "Content-Type: multipart/mixed; boundary=mixed\r\n\r\n--mixed\r\nContent-Type: multipart/alternative; boundary=apple-alternative\r\n\r\n" + appleAlternativeParts("", applePlainWrapperFixture) + "\r\n--mixed\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=sample.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAAECAw==\r\n--mixed--\r\n"
	body, attachments, err := reconstructibleDraft(snapshot)
	if err != nil || body != snapshot.Message.Content || len(attachments) != 1 || attachments[0].name != "sample.bin" || string(attachments[0].data) != "\x00\x01\x02\x03" {
		t.Fatalf("body=%q attachments=%+v err=%v", body, attachments, err)
	}
	snapshot.Source = strings.Replace(snapshot.Source, "Content-Disposition: attachment; filename=sample.bin", "Content-Disposition: inline; filename=sample.bin", 1)
	if _, _, err := reconstructibleDraft(snapshot); err == nil {
		t.Fatal("accepted inline attachment with Apple alternative body")
	}
}

func TestNativeAppleAlternativeDraftSendPreservesExactBody(t *testing.T) {
	f := newNativePlainDraftSendFixture()
	wrapper := strings.Replace(applePlainWrapperFixture, "Hello &amp; 世界<br>Second &lt;line&gt;&#160;end", "World 世界", 1)
	f.draft.source = ae.String("From: alias@example.com\r\nTo: to@example.com\r\nSubject: Hello\r\nContent-Type: multipart/alternative; boundary=apple-alternative\r\n\r\n" + appleAlternativeParts("", wrapper))
	result, err := f.client().SendDraftResult("Personal", "15")
	if err != nil || !result.Submitted || result.CleanupPending || f.sendCalls != 1 || f.originalDeletes != 1 || f.outgoing.fields["ctnt"].Text != "World 世界\n" {
		t.Fatalf("result=%+v err=%v sends=%d deletes=%d outgoing body=%q", result, err, f.sendCalls, f.originalDeletes, f.outgoing.fields["ctnt"].Text)
	}
}
