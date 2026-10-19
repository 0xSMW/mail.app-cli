package mail

import (
	"strings"
	"testing"
)

const appleLeadingBlank = `<p style="margin: 0.0px 0.0px 0.0px 0.0px; font: 12.0px Helvetica; color: #000000; -webkit-text-stroke: #000000; min-height: 14.0px"><font style="font-kerning: none"></font><br></p>`

func appleLeadingBlankFixture(t *testing.T) (*draftSnapshot, string) {
	t.Helper()
	snapshot := appleRelatedFixture(t)
	canonical := snapshot.Message.Content
	snapshot.Source = strings.Replace(snapshot.Source, `<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, appleLeadingBlank+"\n"+`<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, 1)
	const secondCID = "11234567-89AB-CDEF-0123-456789ABCDEF"
	snapshot.Source = strings.Replace(snapshot.Source, appleRelatedObject(appleRelatedCID), appleRelatedObject(appleRelatedCID)+appleRelatedObject(secondCID), 1)
	second := "--related\r\nContent-Type: text/plain; name=second.txt\r\nContent-Disposition: attachment; filename=second.txt\r\nContent-ID: <" + secondCID + ">\r\nContent-Transfer-Encoding: base64\r\n\r\nc2Vjb25kCg==\r\n"
	snapshot.Source = strings.Replace(snapshot.Source, "--related--", second+"--related--", 1)
	snapshot.Message.Content = "\n\u2028" + canonical
	return snapshot, canonical
}

func TestDraftAppleRelatedLeadingDefaultBlankCanonicalBody(t *testing.T) {
	snapshot, canonical := appleLeadingBlankFixture(t)
	body, files, err := reconstructibleDraft(snapshot)
	if err != nil || body != canonical || len(files) != 2 {
		t.Fatalf("body=%q files=%+v err=%v", body, files, err)
	}
	if files[0].name != "re\u0301sume\u0301.bin" || string(files[0].data) != "\x00\x01\x02\x03" || files[1].name != "second.txt" || string(files[1].data) != "second\n" {
		t.Fatalf("lost exact attachment association or bytes: %+v", files)
	}
	// The existing normal readback path remains valid, without requiring a
	// display separator when Mail returns the canonical body directly.
	snapshot.Message.Content = canonical
	body, _, err = reconstructibleDraft(snapshot)
	if err != nil || body != canonical {
		t.Fatalf("normal readback body=%q err=%v", body, err)
	}
}

func TestDraftAppleRelatedLeadingDefaultBlankRejectsUnprovenShapes(t *testing.T) {
	for name, replacement := range map[string]string{
		"empty paragraph":    strings.Split(appleLeadingBlank, ">")[0] + "></p>",
		"font only":          strings.Replace(appleLeadingBlank, "<br>", "", 1),
		"missing font":       strings.Replace(appleLeadingBlank, `<font style="font-kerning: none"></font>`, "", 1),
		"changed height":     strings.Replace(appleLeadingBlank, "14.0px", "15.0px", 1),
		"changed color":      strings.Replace(appleLeadingBlank, "color: #000000", "color: #111111", 1),
		"changed margin":     strings.Replace(appleLeadingBlank, "margin: 0.0px", "margin: 1.0px", 1),
		"changed font style": strings.Replace(appleLeadingBlank, "font-kerning: none", "font-kerning: auto", 1),
		"font text":          strings.Replace(appleLeadingBlank, "</font>", "text</font>", 1),
		"font nested break":  strings.Replace(appleLeadingBlank, "</font>", "<br></font>", 1),
		"extra child":        strings.Replace(appleLeadingBlank, "</p>", "<br></p>", 1),
		"extra whitespace":   strings.Replace(appleLeadingBlank, "</font>", "</font> ", 1),
		"duplicate blank":    appleLeadingBlank + appleLeadingBlank,
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, _ := appleLeadingBlankFixture(t)
			snapshot.Source = strings.Replace(snapshot.Source, appleLeadingBlank, replacement, 1)
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unproven leading blank paragraph")
			}
		})
	}
}

func TestDraftAppleRelatedLeadingDefaultBlankRejectsBodyDrift(t *testing.T) {
	for _, mutation := range []string{"extra separator", "in text separator", "changed whitespace", "missing body", "custom body font", "nonleading blank"} {
		t.Run(mutation, func(t *testing.T) {
			snapshot, _ := appleLeadingBlankFixture(t)
			switch mutation {
			case "extra separator":
				snapshot.Message.Content = "\u2028" + snapshot.Message.Content
			case "in text separator":
				snapshot.Message.Content = strings.Replace(snapshot.Message.Content, "Plain", "Pl\u2028ain", 1)
			case "changed whitespace":
				snapshot.Message.Content += " "
			case "missing body":
				start := strings.Index(snapshot.Source, appleLeadingBlank) + len(appleLeadingBlank)
				end := strings.Index(snapshot.Source[start:], appleRelatedObject(appleRelatedCID)) + start
				snapshot.Source = snapshot.Source[:start] + snapshot.Source[end:]
			case "custom body font":
				snapshot.Source = strings.Replace(snapshot.Source, `face="Helvetica"`, `face="Arial"`, 1)
			case "nonleading blank":
				snapshot.Source = strings.Replace(snapshot.Source, appleLeadingBlank, "", 1)
				snapshot.Source = strings.Replace(snapshot.Source, appleRelatedObject(appleRelatedCID), appleLeadingBlank+appleRelatedObject(appleRelatedCID), 1)
			}
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted changed body or unsupported layout")
			}
		})
	}
	original := appleRelatedFixture(t)
	original.Message.Content = "\n\u2028" + original.Message.Content
	if _, _, err := reconstructibleDraft(original); err == nil {
		t.Fatal("accepted display separator without proven leading blank paragraph")
	}
}
