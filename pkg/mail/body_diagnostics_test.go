package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func readDiagnosticFixture(t *testing.T, filename, message string) (string, *BodyDiagnostics, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	writeEMLX(t, path, message)
	body, diagnostics, err := readEMLXBodyWithDiagnostics(path)
	// The compatibility wrapper must return exactly the same content and errors.
	legacy, legacyErr := readEMLXBody(path)
	if body != legacy || fmt.Sprint(err) != fmt.Sprint(legacyErr) {
		t.Fatalf("wrapper differs: %q, %v vs %q, %v", body, err, legacy, legacyErr)
	}
	return body, diagnostics, err
}

func TestBodyDiagnosticsExposeAlternativeAndImageOmissions(t *testing.T) {
	message := "Content-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\nYour workshop starts Tuesday at 10. Bring the signed form.\n--b\nContent-Type: text/html\n\n<img src=\"https://example.invalid/invitation.png\" alt=\"Workshop Tuesday at 10\"><footer>Unsubscribe</footer>\n--b--\n"
	body, d, err := readDiagnosticFixture(t, "1.emlx", message)
	if err != nil || body != "Unsubscribe" {
		t.Fatalf("body %q, error %v", body, err)
	}
	if d.MIMEPartCount != 3 || d.HTMLImageCount != 1 || d.ExtractedCharCount != 11 || d.PartialFile || d.ExtractionError != "" {
		t.Fatalf("diagnostics %+v", d)
	}
	if len(d.Alternatives) != 2 || d.Alternatives[0].Selected || !d.Alternatives[1].Selected || !strings.Contains(d.Alternatives[0].Text, "signed form") || d.Alternatives[1].Text != body {
		t.Fatalf("alternatives %+v", d.Alternatives)
	}
	if len(d.HTMLImageAltTexts) != 1 || d.HTMLImageAltTexts[0] != "Workshop Tuesday at 10" {
		t.Fatalf("image alt texts %+v", d.HTMLImageAltTexts)
	}
	if len(d.Warnings) != 2 || !strings.Contains(d.Warnings[0], "image pixels") || !strings.Contains(d.Warnings[1], "different extracted text") {
		t.Fatalf("warnings %+v", d.Warnings)
	}
}

func TestBodyDiagnosticsShortTransactionHasNoCompletenessClaim(t *testing.T) {
	for _, message := range []string{"Content-Type: text/plain; charset=utf-8\n\nPaid €5.\n", "Content-Type: text/html; charset=utf-8\n\n<p>Paid €5.</p>\n"} {
		body, d, err := readDiagnosticFixture(t, "1.emlx", message)
		if err != nil || body != "Paid €5." || d.ExtractedCharCount != utf8.RuneCountInString(body) || len(d.Warnings) != 0 || d.MIMEPartCount != 1 {
			t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
		}
	}
}

func TestBodyDiagnosticsPartialProvenance(t *testing.T) {
	body, d, err := readDiagnosticFixture(t, "1.partial.emlx", "Content-Type: text/plain\n\nAvailable text\n")
	if err != nil || body != "Available text" || !d.PartialFile || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], ".partial.emlx") {
		t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
	}
}

func TestBodyDiagnosticsNestedMIMEAndNonText(t *testing.T) {
	message := "Content-Type: multipart/mixed; boundary=outer\n\n--outer\nContent-Type: multipart/alternative; boundary=inner\n\n--inner\nContent-Type: text/plain\n\nplain\n--inner\nContent-Type: text/html\n\n<p>rich</p>\n--inner--\n--outer\nContent-Type: image/png\nContent-ID: <picture>\n\nimage bytes\n--outer\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=invoice.pdf\n\npdf bytes\n--outer--\n"
	body, d, err := readDiagnosticFixture(t, "1.emlx", message)
	if err != nil || body != "rich" || d.MIMEPartCount != 6 || d.NonTextPartCount != 2 || d.ImagePartCount != 1 || d.AttachmentPartCount != 1 {
		t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
	}
	if len(d.Alternatives) != 2 || d.Alternatives[0].Group != "1.1" || d.Alternatives[1].Part != "1.1.2" || !d.Alternatives[1].Selected {
		t.Fatalf("alternatives %+v", d.Alternatives)
	}
}

func TestBodyDiagnosticsKeepParserFailuresAndObservations(t *testing.T) {
	for _, test := range []struct {
		message, errorText string
		parts              int
	}{
		{"Content-Type: multipart/mixed\n\nbody", "no boundary", 1},
		{"Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/html\n\n<img src=x><p>body</p>\n--b\nContent-Type: text/plain; charset=bad-charset\n\ntext\n--b--\n", "unsupported charset", 3},
		{"Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\ntext", "decode text/plain part: unexpected EOF", 2},
	} {
		body, d, err := readDiagnosticFixture(t, "1.partial.emlx", test.message)
		if err == nil || !strings.Contains(err.Error(), test.errorText) || body != "" || d.ExtractionError != err.Error() || d.MIMEPartCount != test.parts || !d.PartialFile || d.ExtractedCharCount != 0 {
			t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
		}
	}
}

func TestBodyDiagnosticsPreserveDepthBound(t *testing.T) {
	message := "Content-Type: text/plain\n\nleaf\n"
	for depth := 0; depth < 10; depth++ {
		boundary := fmt.Sprintf("boundary%d", depth)
		message = fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\n\n--%s\n%s--%s--\n", boundary, boundary, message, boundary)
	}
	_, d, err := readDiagnosticFixture(t, "1.emlx", message)
	if err == nil || !strings.Contains(err.Error(), "too deeply") || d.MIMEPartCount != 9 {
		t.Fatalf("diagnostics %+v, error %v", d, err)
	}
}

func TestBodyDiagnosticsRichMarkupWithoutFetching(t *testing.T) {
	body, d, err := readDiagnosticFixture(t, "1.emlx", "Content-Type: text/html\n\n<p>Receipt</p><img src=\"https://example.invalid/a\" alt=\"Total €5\"><img src=\"cid:b\"><div style=\"background-image:url(https://example.invalid/c)\"></div><canvas></canvas><svg></svg>\n")
	if err != nil || body != "Receipt" || d.HTMLImageCount != 2 || d.RichContentElements != 3 || len(d.Warnings) != 2 || len(d.HTMLImageAltTexts) != 1 {
		t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
	}
}

func TestBodyDiagnosticsPreserveFileBoundAndDamagedLength(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "1.partial.emlx")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxMessageFileSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	body, d, err := readEMLXBodyWithDiagnostics(oversized)
	if err == nil || !strings.Contains(err.Error(), "larger than") || body != "" || !d.PartialFile || d.MIMEPartCount != 0 || d.ExtractionError != err.Error() {
		t.Fatalf("body %q, diagnostics %+v, error %v", body, d, err)
	}
	damaged := filepath.Join(t.TempDir(), "2.partial.emlx")
	if err := os.WriteFile(damaged, []byte("500\nSubject: fictional\n\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, d, err = readEMLXBodyWithDiagnostics(damaged)
	if err == nil || !strings.Contains(err.Error(), "invalid length line") || !d.PartialFile || d.MIMEPartCount != 0 || d.ExtractionError != err.Error() {
		t.Fatalf("diagnostics %+v, error %v", d, err)
	}
}

// BenchmarkBodyDiagnosticsRead measures file read and extraction together;
// fixtures contain no real mail and HTML resources are never loaded.
func BenchmarkBodyDiagnosticsRead(b *testing.B) {
	for _, fixture := range []struct{ name, message string }{
		{"short_transaction", "Content-Type: text/plain\n\nPaid $5.\n"},
		{"large_alternative", "Content-Type: multipart/alternative; boundary=b\n\n--b\nContent-Type: text/plain\n\n" + strings.Repeat("Workshop invitation details. ", 10000) + "\n--b\nContent-Type: text/html\n\n<img src=\"https://example.invalid/invitation\" alt=\"Workshop\"><footer>Unsubscribe</footer>\n--b--\n"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "1.emlx")
			raw := fmt.Sprintf("%d\n%s", len(fixture.message), fixture.message)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := readEMLXBodyWithDiagnostics(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
