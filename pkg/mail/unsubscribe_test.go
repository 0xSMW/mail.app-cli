package mail

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func unsubscribeTestObservation(id, listID, sender, date, url string) UnsubscribeObservation {
	return UnsubscribeObservation{ID: id, Account: "Personal", RFCMessageID: "<" + id + "@example.test>", Mailboxes: []string{"INBOX"}, ListID: listID, Sender: sender, DateReceived: date, HeaderStatus: "available", Complete: true, Destinations: []UnsubscribeDestination{{URL: url, Kind: "unsubscribe", Openable: true, Evidence: []UnsubscribeEvidence{{MessageID: id, Source: "header", Kind: "unsubscribe", Selected: true}}}}}
}
func TestUnsubscribeGroupsListIdentityAndHistoricalControls(t *testing.T) {
	old := unsubscribeTestObservation("1", "news.example.test", "News <same@example.test>", "2026-10-20T00:00:00Z", "https://controls.test/old")
	latest := unsubscribeTestObservation("2", "news.example.test", "Rotating <new@example.test>", "2026-10-22T00:00:00Z", "https://controls.test/new")
	distinct := unsubscribeTestObservation("3", "other.example.test", "News <same@example.test>", "2026-10-21T00:00:00Z", "https://controls.test/other")
	groups := GroupUnsubscribeObservations([]UnsubscribeObservation{old, distinct, latest})
	if len(groups) != 2 {
		t.Fatalf("groups=%+v", groups)
	}
	for _, g := range groups {
		if g.ListID == "news.example.test" {
			if g.Provisional || g.MessageCount != 2 || len(g.Senders) != 2 || g.Latest.ID != "2" || len(g.Destinations) != 2 || !g.DestinationsVary || len(g.Messages) != 2 {
				t.Fatalf("group=%+v", g)
			}
		}
	}
	reversed := GroupUnsubscribeObservations([]UnsubscribeObservation{latest, distinct, old})
	a, _ := json.Marshal(groups)
	b, _ := json.Marshal(reversed)
	if string(a) != string(b) {
		t.Fatalf("unstable grouping %s %s", a, b)
	}
}
func TestUnsubscribeGroupsDuplicateCopiesAndProvenance(t *testing.T) {
	first := unsubscribeTestObservation("1", "news.example.test", "Sender <sender@example.test>", "2026-10-21T00:00:00Z", "https://controls.test/leave")
	folderCopy := first
	folderCopy.Mailboxes = []string{"Archive"}
	rfcCopy := first
	rfcCopy.ID = "2"
	rfcCopy.Mailboxes = []string{"All Mail"}
	rfcCopy.Destinations = []UnsubscribeDestination{{URL: first.Destinations[0].URL, Kind: "preferences", Openable: true, Evidence: []UnsubscribeEvidence{{MessageID: "2", Source: "html", MIMEPart: "1.2", Text: "Preferences", Kind: "preferences", Selected: true}}}}
	groups := GroupUnsubscribeObservations([]UnsubscribeObservation{folderCopy, rfcCopy, first})
	if len(groups) != 1 || groups[0].MessageCount != 1 || len(groups[0].Messages) != 2 || len(groups[0].Destinations) != 1 || groups[0].Destinations[0].Kind != "unsubscribe" || len(groups[0].Destinations[0].Evidence) != 2 {
		t.Fatalf("groups=%+v", groups)
	}
	for _, o := range groups[0].Messages {
		if o.ID == "1" && strings.Join(o.Mailboxes, ",") != "Archive,INBOX" {
			t.Fatalf("memberships=%+v", o)
		}
	}
}
func TestUnsubscribeGroupsFallbackDoesNotCombineDifferentSenders(t *testing.T) {
	a := unsubscribeTestObservation("1", "", "a@example.test", "2026-10-21T00:00:00Z", "https://shared.test/a")
	b := unsubscribeTestObservation("2", "", "b@example.test", "2026-10-21T00:00:00Z", "https://shared.test/b")
	c := unsubscribeTestObservation("3", "", "a@example.test", "2026-10-21T00:00:00Z", "https://other.test/c")
	groups := GroupUnsubscribeObservations([]UnsubscribeObservation{a, b, c})
	if len(groups) != 3 {
		t.Fatalf("groups=%+v", groups)
	}
	for _, g := range groups {
		if !g.Provisional || g.KeySource != "sender-header-hosts" {
			t.Fatalf("group=%+v", g)
		}
	}
}
func TestUnsubscribeLocalShowAndBatchNeverRequestMessageAutomation(t *testing.T) {
	c, root, events := localReadFixture(t)
	owner := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages")
	source := "List-Id: Synthetic News <NEWS.Example.test>\nList-Unsubscribe: <https://controls.test/leave>, <mailto:leave@example.test>\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\nContent-Type: text/html\n\n<p>Unsubscribe <a href=\"https://body.test/leave\">here</a></p>"
	writeEMLX(t, filepath.Join(owner, "101.emlx"), source)
	writeEMLX(t, filepath.Join(owner, "103.partial.emlx"), source)
	o, err := c.GetUnsubscribeJSON("Work", "INBOX", "101")
	if err != nil || o == nil || !o.Complete || o.HeaderStatus != "available" || len(o.Destinations) != 3 || !o.HasHTTPSUnsubscribe || !o.OneClickAdvertised || o.ListID != "news.example.test" {
		t.Fatalf("observation=%+v err=%v", o, err)
	}
	observations, err := c.ReadLocalUnsubscribeMessages([]ScanMessage{{Message: Message{ID: "101", Account: "Work", Mailbox: "INBOX"}, Mailboxes: []string{"INBOX", "Archive"}}, {Message: Message{ID: "103", Account: "Work", Mailbox: "INBOX"}}, {Message: Message{ID: "105", Account: "Work", Mailbox: "INBOX"}}})
	if err != nil || len(observations) != 3 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	for _, o := range observations {
		switch o.ID {
		case "101":
			if !o.Complete || len(o.Destinations) != 3 {
				t.Fatalf("complete=%+v", o)
			}
		case "103":
			if o.Complete || o.HeaderStatus != "available" || len(o.Destinations) != 3 || len(o.Warnings) == 0 {
				t.Fatalf("partial=%+v", o)
			}
		case "105":
			if o.Complete || o.HeaderStatus != "unavailable" || o.Error == "" {
				t.Fatalf("missing=%+v", o)
			}
		}
	}
	if events.Load() != 0 {
		t.Fatalf("native/JXA calls=%d", events.Load())
	}
}
func TestUnsubscribeCancellationLeavesUnattemptedPending(t *testing.T) {
	c, _, events := localReadFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observations, err := c.WithContext(ctx).ReadLocalUnsubscribeMessages([]ScanMessage{{Message: Message{ID: "101", Account: "Work", Mailbox: "INBOX"}}})
	if err != context.Canceled || len(observations) != 0 || events.Load() != 0 {
		t.Fatalf("observations=%+v err=%v calls=%d", observations, err, events.Load())
	}
}
func TestUnsubscribeMalformedEnvelopeStillKeepsBodyControls(t *testing.T) {
	c, root, events := localReadFixture(t)
	path := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages", "101.emlx")
	writeEMLX(t, path, "Subject: =?bad-charset?Q?broken?=\nList-Id: invalid\nContent-Type: text/html\n\n<a href=\"https://body.test/unsubscribe\">Unsubscribe</a>")
	o, err := c.GetUnsubscribeJSON("Work", "INBOX", "101")
	if err != nil || o == nil || o.Complete || o.HeaderStatus != "available" || len(o.Destinations) != 1 || o.Error == "" || o.ListID != "" || events.Load() != 0 {
		t.Fatalf("observation=%+v err=%v", o, err)
	}
}
func TestUnsubscribeListIDValidation(t *testing.T) {
	for raw, want := range map[string]string{"News <NEWS.Example.test>": "news.example.test", "<one.example.test>": "one.example.test", "bad": "", "<space bad.example>": "", "<a@example.test>": "", "<.example.test>": "", "<one.example.test> suffix": ""} {
		if got := canonicalListID(raw); got != want {
			t.Fatalf("raw=%q got=%q want=%q", raw, got, want)
		}
	}
}

type unsubscribeCancelAfterReadContext struct {
	context.Context
	cancel      context.CancelFunc
	filesBefore int64
}

func (ctx unsubscribeCancelAfterReadContext) Err() error {
	if LocalReadStatistics().FilesRead > ctx.filesBefore {
		ctx.cancel()
	}
	return ctx.Context.Err()
}
func TestUnsubscribeCancellationRetainsAttemptedObservations(t *testing.T) {
	c, _, events := localReadFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bounded := unsubscribeCancelAfterReadContext{Context: ctx, cancel: cancel, filesBefore: LocalReadStatistics().FilesRead}
	inputs := []ScanMessage{{Message: Message{ID: "101", Account: "Work", Mailbox: "INBOX"}}, {Message: Message{ID: "103", Account: "Work", Mailbox: "INBOX"}}, {Message: Message{ID: "104", Account: "Work", Mailbox: "INBOX"}}, {Message: Message{ID: "105", Account: "Work", Mailbox: "INBOX"}}}
	observations, err := c.WithContext(bounded).ReadLocalUnsubscribeMessages(inputs)
	if err != context.Canceled || len(observations) == 0 || len(observations) > len(inputs) || events.Load() != 0 {
		t.Fatalf("observations=%+v err=%v calls=%d", observations, err, events.Load())
	}
	for _, o := range observations {
		if o.ID == "" || o.Account != "Work" || len(o.Mailboxes) != 1 || o.Mailboxes[0] != "INBOX" {
			t.Fatalf("placeholder/mismatched observation=%+v", o)
		}
	}
}
func TestUnsubscribeLocalShowUnavailableSourceIsExplicit(t *testing.T) {
	c, _, events := localReadFixture(t)
	o, err := c.GetUnsubscribeJSON("Work", "INBOX", "105")
	if err != nil || o == nil || o.Complete || o.HeaderStatus != "unavailable" || o.Error == "" || len(o.Destinations) != 0 || events.Load() != 0 {
		t.Fatalf("observation=%+v err=%v calls=%d", o, err, events.Load())
	}
	o, err = c.GetUnsubscribeJSON("Other", "INBOX", "101")
	if err == nil || o == nil || o.Complete || o.HeaderStatus != "unavailable" {
		t.Fatalf("scope mismatch observation=%+v err=%v", o, err)
	}
}
func TestUnsubscribeHeaderListIDRetainsRawAdvertisement(t *testing.T) {
	raw := "Synthetic News <NEWS.Example.test>"
	m := &Message{}
	populateUnsubscribe(m, map[string][]string{"lIsT-iD": {raw}})
	if m.Unsubscribe.ListID != raw || canonicalListID(m.Unsubscribe.ListID) != "news.example.test" {
		t.Fatalf("unsubscribe=%+v", m.Unsubscribe)
	}
	populateUnsubscribe(m, map[string][]string{"List-Id": {"<one.example.test>", "<two.example.test>"}})
	if canonicalListID(m.Unsubscribe.ListID) != "" || m.Unsubscribe.ListID == "" {
		t.Fatalf("ambiguous advertisement must remain raw: %+v", m.Unsubscribe)
	}
}

func TestUnsubscribeGroupsNewestListMessageWithoutControls(t *testing.T) {
	old := unsubscribeTestObservation("1", "news.example.test", "older@example.test", "2026-10-20T00:00:00Z", "https://controls.test/historical")
	current := unsubscribeTestObservation("2", "news.example.test", "current@example.test", "2026-10-22T00:00:00Z", "")
	current.Destinations = []UnsubscribeDestination{}
	groups := GroupUnsubscribeObservations([]UnsubscribeObservation{old, current})
	if len(groups) != 1 {
		t.Fatalf("groups=%+v", groups)
	}
	g := groups[0]
	if g.Latest.ID != "2" || len(g.Latest.Destinations) != 0 || g.MessageCount != 2 || len(g.Messages) != 2 || len(g.Destinations) != 1 || g.Destinations[0].URL != "https://controls.test/historical" || !g.DestinationsVary {
		t.Fatalf("group=%+v", g)
	}
	if groups := GroupUnsubscribeObservations([]UnsubscribeObservation{current}); len(groups) != 0 {
		t.Fatalf("no-control observations created groups: %+v", groups)
	}
}
func TestUnsubscribeGroupsDoNotGuessEmptyObservationsIntoHostGroups(t *testing.T) {
	old := unsubscribeTestObservation("1", "", "same@example.test", "2026-10-20T00:00:00Z", "https://controls.test/leave")
	current := unsubscribeTestObservation("2", "", "same@example.test", "2026-10-22T00:00:00Z", "")
	current.Destinations = []UnsubscribeDestination{}
	groups := GroupUnsubscribeObservations([]UnsubscribeObservation{old, current})
	if len(groups) != 1 || groups[0].Latest.ID != "1" || groups[0].MessageCount != 1 || len(groups[0].Messages) != 1 {
		t.Fatalf("guessed host membership: %+v", groups)
	}
}

func TestUnsubscribeUnsupportedBodyIsUnknownWhileImageOnlyHTMLIsInspected(t *testing.T) {
	for _, test := range []struct {
		name, body string
		complete   bool
	}{
		{"encrypted body", "Content-Type: application/pkcs7-mime\n\nopaque encrypted bytes", false},
		{"image-only HTML", "Content-Type: text/html\n\n<a href=\"https://example.test/leave\"><img src=\"cid:button\"></a>", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "42.emlx")
			writeEMLX(t, path, "List-Unsubscribe: <https://example.test/header>\n"+test.body)
			m := Message{ID: "42", Account: "Work", Mailbox: "INBOX"}
			_, err := NewClient().hydrateLocalMessage(&m, path)
			if err != nil {
				t.Fatal(err)
			}
			o := unsubscribeObservation(m, []string{"INBOX"})
			if o.Complete != test.complete || len(o.Destinations) == 0 {
				t.Fatalf("observation=%+v", o)
			}
			if !test.complete && len(o.Warnings) == 0 {
				t.Fatal("unsupported body omitted its coverage warning")
			}
		})
	}
}
