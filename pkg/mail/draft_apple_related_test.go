package mail

import (
	"mime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const appleRelatedCID = "01234567-89AB-CDEF-0123-456789ABCDEF"

func appleRelatedFont(face, text string) string {
	styleFace := face
	if strings.Contains(face, " ") {
		styleFace = "'" + face + "'"
	}
	return `<font face="` + face + `" size="3" color="#000000" style="font: 12.0px ` + styleFace + `; font-kerning: none; color: #000000; -webkit-text-stroke: 0px #000000">` + text + `</font>`
}

func appleRelatedObject(cid string) string {
	return `<span class="Apple-string-attachment"><object type="application/x-apple-msg-attachment" width="102" data="cid:` + cid + `"></object></span>`
}

func appleRelatedFixture(t *testing.T) *draftSnapshot {
	t.Helper()
	paragraphs := `<p style="margin: 0.0px 0.0px 0.0px 0.0px">` + appleRelatedFont("Helvetica", "Plain &lt;line&gt; &amp; symbols.") + "</p>\n" + `<p style="margin: 0.0px 0.0px 0.0px 0.0px">` + appleRelatedFont("Helvetica", "Unicode: café ") + appleRelatedFont("PingFang SC", "日本語") + appleRelatedFont("Helvetica", " ") + appleRelatedFont("Apple Color Emoji", "🚀") + "</p>" + appleRelatedObject(appleRelatedCID)
	wrapper := strings.Replace(applePlainWrapperFixture, "\n\nHello &amp; 世界<br>Second &lt;line&gt;&#160;end<br>\n\n", paragraphs, 1)
	filename := "re\u0301sume\u0301.bin"
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	related := "--related\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + wrapper + "\r\n--related\r\nContent-Type: application/macbinary; x-unix-mode=0666; name=\"" + mime.BEncoding.Encode("utf-8", filename) + "\"\r\nContent-Disposition: " + disposition + "\r\nContent-Transfer-Encoding: base64\r\nContent-ID: <" + appleRelatedCID + ">\r\n\r\nAAECAw==\r\n--related--\r\n"
	snapshot := appleAlternativeFixture(t)
	snapshot.Message.Content = "\nPlain <line> & symbols.\nUnicode: café 日本語 🚀\n"
	snapshot.Source = snapshot.Source[:strings.Index(snapshot.Source, "--apple-alternative")] + "--apple-alternative\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n\r\n--apple-alternative\r\nContent-Type: multipart/related; type=\"text/html\"; boundary=related\r\n\r\n" + related + "\r\n--apple-alternative--\r\n"
	return snapshot
}

func TestDraftAppleRelatedPreservesPlainBodyAndExactAttachmentBytes(t *testing.T) {
	snapshot := appleRelatedFixture(t)
	body, attachments, err := reconstructibleDraft(snapshot)
	if err != nil || body != snapshot.Message.Content || len(attachments) != 1 || attachments[0].name != "re\u0301sume\u0301.bin" || string(attachments[0].data) != "\x00\x01\x02\x03" {
		t.Fatalf("body=%q attachments=%+v err=%v", body, attachments, err)
	}
	file := filepath.Join(t.TempDir(), "résumé.bin")
	if err := os.WriteFile(file, attachments[0].data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareDraftMIMEAttachments([]string{file}, attachments); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("different exported bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareDraftMIMEAttachments([]string{file}, attachments); err == nil {
		t.Fatal("accepted different exported bytes under a MacBinary content type")
	}
}

func TestDraftAppleRelatedRejectsFormattingAndUnprovenAttachments(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"different text", "Plain &lt;line&gt;", "Different &lt;line&gt;"},
		{"different font", `face="Helvetica"`, `face="Arial"`},
		{"different font size", `size="3"`, `size="4"`},
		{"different color", `color="#000000"`, `color="#FF0000"`},
		{"different font style", "font-kerning: none;", "font-weight: bold;"},
		{"different paragraph", "margin: 0.0px 0.0px 0.0px 0.0px", "margin: 10px"},
		{"nested link", "Plain &lt;line&gt; &amp; symbols.", `<a href="https://example.test">Plain &lt;line&gt; &amp; symbols.</a>`},
		{"nested span", "Plain &lt;line&gt; &amp; symbols.", `<span>Plain &lt;line&gt; &amp; symbols.</span>`},
		{"inline image", `<object type="application/x-apple-msg-attachment"`, `<img type="application/x-apple-msg-attachment"`},
		{"other object type", "application/x-apple-msg-attachment", "application/pdf"},
		{"remote reference", "cid:" + appleRelatedCID, "https://example.test/file"},
		{"unresolved reference", "cid:" + appleRelatedCID, "cid:11234567-89AB-CDEF-0123-456789ABCDEF"},
		{"missing reference", appleRelatedObject(appleRelatedCID), ""},
		{"duplicate reference", appleRelatedObject(appleRelatedCID), appleRelatedObject(appleRelatedCID) + appleRelatedObject(appleRelatedCID)},
		{"inline disposition", "Content-Disposition: attachment", "Content-Disposition: inline"},
		{"different filename", "filename*=utf-8''re%CC%81sume%CC%81.bin", "filename=other.bin"},
		{"remote file part", "Content-Type: application/macbinary;", "Content-Type: message/external-body;"},
		{"signed file part", "Content-Type: application/macbinary;", "Content-Type: application/pkcs7-mime;"},
		{"extra attachment header", "Content-ID: <", "Content-Location: https://example.test/file\r\nContent-ID: <"},
		{"missing attachment cid", "Content-ID: <" + appleRelatedCID + ">\r\n", ""},
		{"related root type", `type="text/html"; boundary=related`, `type="image/png"; boundary=related`},
		{"encoded related root", "Content-Type: multipart/related;", "Content-Transfer-Encoding: base64\r\nContent-Type: multipart/related;"},
		{"nested related", "Content-Type: text/html; charset=utf-8", "Content-Type: multipart/related; boundary=nested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := appleRelatedFixture(t)
			if !strings.Contains(snapshot.Source, tc.before) {
				t.Fatalf("fixture missing mutation target %q", tc.before)
			}
			snapshot.Source = strings.Replace(snapshot.Source, tc.before, tc.after, 1)
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unsupported formatting or attachment semantics")
			}
		})
	}
}

func TestDraftAppleRelatedRejectsDuplicateOrMissingAttachmentParts(t *testing.T) {
	for _, operation := range []string{"duplicate", "missing", "extra body", "body after attachment"} {
		t.Run(operation, func(t *testing.T) {
			snapshot := appleRelatedFixture(t)
			start := strings.Index(snapshot.Source, "--related\r\nContent-Type: application/macbinary")
			end := strings.Index(snapshot.Source[start:], "--related--") + start
			part := snapshot.Source[start:end]
			switch operation {
			case "duplicate":
				snapshot.Source = snapshot.Source[:end] + part + snapshot.Source[end:]
			case "missing":
				snapshot.Source = snapshot.Source[:start] + snapshot.Source[end:]
			case "extra body":
				snapshot.Source = snapshot.Source[:end] + "--related\r\nContent-Type: text/html\r\n\r\nextra\r\n" + snapshot.Source[end:]
			case "body after attachment":
				snapshot.Source = strings.Replace(snapshot.Source, "</blockquote>", "<p style=\"margin: 0.0px 0.0px 0.0px 0.0px\">"+appleRelatedFont("Helvetica", "after")+"</p></blockquote>", 1)
			}
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unproven part or placement")
			}
		})
	}
}
