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

func TestDraftAppleRelatedExactBlankParagraphPositions(t *testing.T) {
	const first = "Plain <line> & symbols.\n"
	const second = "Unicode: café 日本語 🚀\n"
	for _, position := range []string{"leading", "interior", "consecutive", "trailing", "leading and interior"} {
		t.Run(position, func(t *testing.T) {
			snapshot, _ := appleLeadingBlankFixture(t)
			snapshot.Source = strings.Replace(snapshot.Source, appleLeadingBlank+"\n", "", 1)
			canonical := first + second
			switch position {
			case "leading":
				snapshot.Source = strings.Replace(snapshot.Source, `<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, appleLeadingBlank+`<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, 1)
				canonical = "\n" + canonical
			case "interior", "leading and interior":
				snapshot.Source = strings.Replace(snapshot.Source, "</p>\n", "</p>\n"+appleLeadingBlank+"\n", 1)
				canonical = first + "\n" + second
				if position == "leading and interior" {
					snapshot.Source = strings.Replace(snapshot.Source, `<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, appleLeadingBlank+`<p style="margin: 0.0px 0.0px 0.0px 0.0px">`, 1)
					canonical = "\n" + canonical
				}
			case "consecutive":
				snapshot.Source = strings.Replace(snapshot.Source, "</p>\n", "</p>\n"+appleLeadingBlank+appleLeadingBlank+"\n", 1)
				canonical = first + "\n\n" + second
			case "trailing":
				snapshot.Source = strings.Replace(snapshot.Source, appleRelatedObject(appleRelatedCID), appleLeadingBlank+appleRelatedObject(appleRelatedCID), 1)
				canonical += "\n"
			}
			rendered, ok := attachmentParagraphReadback(canonical)
			if !ok {
				t.Fatal("invalid canonical fixture")
			}
			snapshot.Message.Content = rendered
			body, files, err := reconstructibleDraft(snapshot)
			if err != nil || body != canonical || len(files) != 2 || string(files[0].data) != "\x00\x01\x02\x03" || string(files[1].data) != "second\n" {
				t.Fatalf("body=%q files=%+v err=%v", body, files, err)
			}
			for _, changed := range []string{strings.Replace(rendered, "\u2028", "", 1), strings.Replace(rendered, "Plain", "Pl\u2028ain", 1), rendered + " "} {
				snapshot.Message.Content = changed
				if _, _, err := reconstructibleDraft(snapshot); err == nil {
					t.Fatalf("accepted altered rendering %q", changed)
				}
			}
		})
	}
}

func TestDraftAppleRelatedBlankRenderingRejectsUnprovenParagraphBreaks(t *testing.T) {
	for _, extra := range []string{`<p style="margin: 0.0px 0.0px 0.0px 0.0px"></p>`, `<p style="margin: 0.0px 0.0px 0.0px 0.0px">` + appleRelatedFont("Helvetica", "<br>") + `</p>`} {
		snapshot, _ := appleLeadingBlankFixture(t)
		snapshot.Source = strings.Replace(snapshot.Source, appleLeadingBlank, appleLeadingBlank+extra, 1)
		if _, _, err := reconstructibleDraft(snapshot); err == nil {
			t.Fatal("accepted additional unproven blank paragraph structure")
		}
	}
}

func TestDraftAppleRelatedOmittedBodyReconstructionDoesNotAuthorWrapperLF(t *testing.T) {
	// This is the captured Mail default paragraph/font/CID structure, with
	// sanitized text and identities. Mail's ctnt adds LF before the first p;
	// that LF is not a paragraph in the source.
	const requested = "Plain <line> & symbols.\nUnicode: café 日本語 🚀\n"
	original := appleRelatedFixture(t)
	if original.Message.Content != "\n"+requested {
		t.Fatal("fixture lacks observed Mail wrapper readback")
	}
	body, files, err := reconstructibleDraft(original)
	if err != nil || body != requested || len(files) != 1 {
		t.Fatalf("initial reconstruction=%q files=%+v err=%v", body, files, err)
	}
	// Omitting --body reuses this returned body. Its exact rendered form must
	// remain the original one-wrapper-LF readback, without a blank paragraph.
	recreated, ok := attachmentParagraphReadback(body)
	if !ok || recreated != original.Message.Content {
		t.Fatalf("omit-body recreation changed readback: %q", recreated)
	}
	updated := *original
	updated.Message = new(Message)
	*updated.Message = *original.Message
	updated.Message.Content = recreated
	body, _, err = reconstructibleDraft(&updated)
	if err != nil || body != requested {
		t.Fatalf("send reconstruction added authored blank: body=%q err=%v", body, err)
	}

	// A deliberate leading blank has an actual exact blank paragraph in the
	// captured wrapper shape. It remains authored text across the same chain.
	authored, canonical := appleLeadingBlankFixture(t)
	body, _, err = reconstructibleDraft(authored)
	if err != nil || body != canonical || body != "\n"+requested {
		t.Fatalf("authored leading blank lost: body=%q err=%v", body, err)
	}
	recreated, ok = attachmentParagraphReadback(body)
	if !ok || recreated != authored.Message.Content {
		t.Fatalf("authored blank recreation changed: %q", recreated)
	}
}

func TestDraftAppleRelatedNormalParagraphCanonicalizationRejectsBodyDrift(t *testing.T) {
	for _, mutation := range []string{"extra leading LF", "extra trailing LF", "missing terminal LF", "separator prefix", "changed text", "changed whitespace"} {
		t.Run(mutation, func(t *testing.T) {
			snapshot := appleRelatedFixture(t)
			switch mutation {
			case "extra leading LF":
				snapshot.Message.Content = "\n" + snapshot.Message.Content
			case "extra trailing LF":
				snapshot.Message.Content += "\n"
			case "missing terminal LF":
				snapshot.Message.Content = strings.TrimSuffix(snapshot.Message.Content, "\n")
			case "separator prefix":
				snapshot.Message.Content = "\n\u2028" + snapshot.Message.Content
			case "changed text":
				snapshot.Message.Content = strings.Replace(snapshot.Message.Content, "Plain", "Changed", 1)
			case "changed whitespace":
				snapshot.Message.Content = strings.Replace(snapshot.Message.Content, "Plain", " Plain", 1)
			}
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted incomplete normal paragraph body relationship")
			}
		})
	}
}
