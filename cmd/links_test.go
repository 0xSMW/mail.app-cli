package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func linkTestMessage() *mail.Message {
	return &mail.Message{
		ID: "42", Account: "Example Account", Mailbox: "INBOX", Content: "Read here. Unsubscribe.",
		LinkStatus:  &mail.LinkExtractionStatus{Complete: true, Source: "disk"},
		Unsubscribe: &mail.MessageUnsubscribe{HeaderStatus: "available", OneClickAdvertised: true},
		Links: []mail.MessageLink{
			{URL: "https://example.test/article", Text: "Read here", Source: "html", MIMEPart: "1.2", Selected: true, Kind: "link", Openable: true},
			{URL: "https://example.test/remove?token=a%2Fb&list=one", Text: "Unsubscribe", Source: "html", MIMEPart: "1.2", Selected: true, Kind: "unsubscribe", Openable: true},
			{URL: "mailto:remove@example.test?subject=unsubscribe", Text: "List-Unsubscribe", Source: "header", Kind: "unsubscribe", Openable: true},
			{URL: "https://example.test/settings", Text: "Manage preferences", Source: "plain", MIMEPart: "1.1", Kind: "preferences", Openable: true},
		},
	}
}

func TestLinkResultFilterPreservesProvenanceAndDoesNotMutateMessage(t *testing.T) {
	m := linkTestMessage()
	got := linkResult(m, true)
	if len(got.Links) != 3 || len(m.Links) != 4 || got.Links[1].Source != "header" || got.Links[2].Selected {
		t.Fatalf("filtered = %+v; original = %+v", got, m)
	}
	got.Links[0].Text = "changed"
	if m.Links[1].Text != "Unsubscribe" {
		t.Fatal("filter aliases the message's link slice")
	}
	empty := linkResult(&mail.Message{ID: "43"}, true)
	encoded, err := json.Marshal(empty)
	if err != nil || !strings.Contains(string(encoded), `"links":[]`) {
		t.Fatalf("empty links must be an array: %s (%v)", encoded, err)
	}
}

func TestLinksOutputRetainsPartialDataAndExactURLs(t *testing.T) {
	for _, complete := range []bool{true, false} {
		m := linkTestMessage()
		m.LinkStatus.Complete = complete
		var stdout, stderr bytes.Buffer
		w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "links", mail.SchemaVersion)
		if err != nil {
			t.Fatal(err)
		}
		a := &application{writer: w}
		err = a.writeMessageLinks(m, true, nil)
		if complete && err != nil {
			t.Fatal(err)
		}
		if !complete && (err == nil || clierr.Classify(err).Code != clierr.CodePartial) {
			t.Fatalf("partial output error = %v", err)
		}
		var envelope struct {
			OK   bool               `json:"ok"`
			Data messageLinksResult `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("%v: %s", err, stdout.String())
		}
		if envelope.OK != complete || len(envelope.Data.Links) != 3 || envelope.Data.Links[0].URL != m.Links[1].URL {
			t.Fatalf("envelope lost partial data or exact URL: %s", stdout.String())
		}
	}
}

func TestLinkHumanOutputPreservesFullURLsAndEscapesControls(t *testing.T) {
	m := linkTestMessage()
	m.Links[1].URL += "&long=" + strings.Repeat("x", 1200)
	m.Links[1].Text = "Unsubscribe\x1b[2J\nspoof"
	var b bytes.Buffer
	p := &output.Printer{Out: &b}
	renderLinkInventory(p, m.Links, true)
	renderLinkStatus(p, &mail.LinkExtractionStatus{Complete: false}, m.Unsubscribe)
	got := b.String()
	for _, want := range []string{m.Links[1].URL, `Unsubscribe\u001b[2J\u000aspoof`, "plain part 1.1, alternative", "incomplete", "eligibility is unverified"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "https://example.test/article") {
		t.Fatalf("unsafe controls or unfiltered article in output: %q", got)
	}
}

func TestLinkHumanOutputEscapesFormatControlsWithoutChangingJSON(t *testing.T) {
	m := &mail.Message{Links: []mail.MessageLink{{
		URL:  "https://example.test/\u202eevil\u2069?token=\U000e0001",
		Text: "Unsubscribe\u2066 now\u200d", Source: "html", Kind: "unsubscribe", Openable: true,
	}}}
	var b bytes.Buffer
	renderLinkInventory(&output.Printer{Out: &b}, m.Links, false)
	for _, want := range []string{`\u202e`, `\u2069`, `\U000e0001`, `\u2066`, `\u200d`} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("format control %s was not escaped: %q", want, b.String())
		}
	}
	for _, control := range []rune{'\u202e', '\u2069', '\U000e0001', '\u2066', '\u200d'} {
		if strings.ContainsRune(b.String(), control) {
			t.Fatalf("format control %U reached terminal output", control)
		}
	}
	encoded, err := json.Marshal(linkResult(m, false))
	if err != nil {
		t.Fatal(err)
	}
	var decoded messageLinksResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Links[0].URL != m.Links[0].URL || decoded.Links[0].Text != m.Links[0].Text {
		t.Fatalf("JSON changed exact link data: %s", encoded)
	}
}

func TestShowLinksHumanOutputAndMetadataIsolation(t *testing.T) {
	m := linkTestMessage()
	for _, tc := range []struct{ metadata, all, article, unsubscribe bool }{
		{false, false, false, true}, {false, true, true, true}, {true, false, false, false},
	} {
		var b bytes.Buffer
		renderMessageWithLinks(m, tc.metadata, tc.all)(&output.Printer{Out: &b})
		got := b.String()
		if strings.Contains(got, m.Links[0].URL) != tc.article || strings.Contains(got, m.Links[1].URL) != tc.unsubscribe {
			t.Fatalf("mode %+v: %s", tc, got)
		}
		if tc.all && strings.Count(got, m.Links[1].URL) != 1 {
			t.Fatal("all-links view repeats unsubscribe destination")
		}
	}
}

func TestLinksCommandUsageIsReadOnlyAndRejectsInvalidSelection(t *testing.T) {
	a := newApplication()
	if a.linksCmd.Annotations[annotationMutation] != "" {
		t.Fatal("links is marked as a mutation")
	}
	for _, args := range [][]string{{"links"}, {"links", "bad"}, {"links", "1", "2"}, {"show", "1", "--links", "--metadata-only"}} {
		code, _, stderr := run(t, args...)
		if code != 1 {
			t.Fatalf("%v: exit %d, %s", args, code, stderr)
		}
	}
}
