package mail

import (
	"fmt"
	"strings"
	"testing"
)

func appleRelatedInMixed(t *testing.T, snapshot *draftSnapshot, outerFile bool) {
	t.Helper()
	header := strings.Index(snapshot.Source, "Content-Type:")
	body := strings.Index(snapshot.Source, "\r\n\r\n") + 4
	if header < 0 || body < 4 {
		t.Fatal("fixture source lacks MIME headers")
	}
	mixed := snapshot.Source[:header] + "Content-Type: multipart/mixed; boundary=outer-mixed\r\n\r\n--outer-mixed\r\nContent-Type: multipart/alternative; boundary=apple-alternative\r\n\r\n" + snapshot.Source[body:]
	if outerFile {
		mixed += "\r\n--outer-mixed\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=outer.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAP8K\r\n"
	}
	snapshot.Source = mixed + "\r\n--outer-mixed--\r\n"
}

func TestDraftAppleMixedRelatedCanonicalBody(t *testing.T) {
	for _, leading := range []bool{false, true} {
		for _, outerFile := range []bool{false, true} {
			t.Run(fmt.Sprintf("leading=%t/outerFile=%t", leading, outerFile), func(t *testing.T) {
				snapshot := appleRelatedFixture(t)
				canonical := "Plain <line> & symbols.\nUnicode: café 日本語 🚀\n"
				count := 1
				if leading {
					snapshot, canonical = appleLeadingBlankFixture(t)
					count = 2
				}
				appleRelatedInMixed(t, snapshot, outerFile)
				if outerFile {
					count++
				}
				body, files, err := reconstructibleDraft(snapshot)
				if err != nil || body != canonical || len(files) != count {
					t.Fatalf("body=%q files=%+v err=%v", body, files, err)
				}
				if files[0].name != "re\u0301sume\u0301.bin" || string(files[0].data) != "\x00\x01\x02\x03" {
					t.Fatalf("lost related payload: %+v", files)
				}
				if leading && (files[1].name != "second.txt" || string(files[1].data) != "second\n") {
					t.Fatalf("lost second related payload: %+v", files)
				}
				if outerFile && (files[count-1].name != "outer.bin" || string(files[count-1].data) != "\x00\xff\n") {
					t.Fatalf("lost outer payload: %+v", files)
				}
			})
		}
	}
}

func TestDraftAppleMixedRelatedStillRejectsBodyAndMIMEDrift(t *testing.T) {
	for _, mutation := range []string{"stored body", "HTML body", "extra body", "extra alternative", "malformed outer file", "inline outer file", "truncated outer"} {
		t.Run(mutation, func(t *testing.T) {
			snapshot := appleRelatedFixture(t)
			appleRelatedInMixed(t, snapshot, true)
			switch mutation {
			case "stored body":
				snapshot.Message.Content += "\n"
			case "HTML body":
				snapshot.Source = strings.Replace(snapshot.Source, "Plain &lt;line&gt;", "Changed &lt;line&gt;", 1)
			case "extra body":
				snapshot.Source = strings.Replace(snapshot.Source, "--outer-mixed--", "--outer-mixed\r\nContent-Type: text/plain\r\n\r\nadditional body\r\n--outer-mixed--", 1)
			case "extra alternative":
				snapshot.Source = strings.Replace(snapshot.Source, "--outer-mixed--", "--outer-mixed\r\nContent-Type: multipart/alternative; boundary=other\r\n\r\n--other--\r\n--outer-mixed--", 1)
			case "malformed outer file":
				snapshot.Source = strings.Replace(snapshot.Source, "AP8K", "%%%", 1)
			case "inline outer file":
				snapshot.Source = strings.Replace(snapshot.Source, "attachment; filename=outer.bin", "inline; filename=outer.bin", 1)
			case "truncated outer":
				snapshot.Source = strings.TrimSuffix(snapshot.Source, "--outer-mixed--\r\n")
			}
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unproven mixed body or MIME payload")
			}
		})
	}
}

func TestDraftMixedPlainBodyStillRequiresExactReadback(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	header := strings.Index(snapshot.Source, "Content-Type:")
	snapshot.Source = snapshot.Source[:header] + "Content-Type: multipart/mixed; boundary=plain-mixed\r\n\r\n--plain-mixed\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n\r\n--plain-mixed--\r\n"
	if _, _, err := reconstructibleDraft(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Message.Content = "\n" + snapshot.Message.Content
	if _, _, err := reconstructibleDraft(snapshot); err == nil {
		t.Fatal("plain body bypassed readback validation")
	}
}
