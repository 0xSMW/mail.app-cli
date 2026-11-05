package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func recipientTestFile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "message.partial.emlx")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecipientHeaderReaderAcceptsCompleteHeadersWithoutBody(t *testing.T) {
	headers := "To: =?UTF-8?B?5p2O6Zu3?= <li@example.com>, Café <cafe@example.com>\r\nTo: Friends: second@example.com, third@example.com;\r\nCc: broken <\r\nDelivered-To: first@example.com\r\nDelivered-To: second@example.com\r\nX-Original-To: original@example.com\r\n\r\n"
	for _, length := range []int64{int64(len(headers)), int64(len(headers) + 500), 1 << 60} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			header, err := readEMLXRecipientHeaders(recipientTestFile(t, fmt.Sprintf("%d\n%s", length, headers)))
			if err != nil {
				t.Fatal(err)
			}
			m := Message{}
			populateRecipientHeaders(&m, header, "disk")
			if !reflect.DeepEqual(m.ToRecipients, []string{"li@example.com", "cafe@example.com", "second@example.com", "third@example.com"}) {
				t.Fatalf("To = %v", m.ToRecipients)
			}
			if !reflect.DeepEqual(m.DeliveredTo, []string{"first@example.com", "second@example.com"}) || !reflect.DeepEqual(m.OriginalTo, []string{"original@example.com"}) {
				t.Fatalf("delivery headers lost order or identity: %+v", m)
			}
			if m.RecipientStatus.To.State != "available" || m.RecipientStatus.To.Source != "disk" || m.RecipientStatus.Cc.State != "unavailable" || m.RecipientStatus.Cc.Error == "" || len(m.CcRecipients) != 0 || m.RecipientStatus.Bcc.State != "available" {
				t.Fatalf("independent parse status = %+v", m.RecipientStatus)
			}
		})
	}
}

func TestRecipientHeaderReaderRejectsIncompleteOrOutOfBoundsHeaders(t *testing.T) {
	for name, data := range map[string]string{
		"no terminator":                       "100\nTo: hidden@example.com\n",
		"truncated line":                      "100\nTo: hidden@example.com",
		"declared boundary before terminator": "23\nTo: hidden@example.com\n\n",
		"oversized headers":                   fmt.Sprintf("%d\nX-Large: %s\nTo: hidden@example.com\n\n", maxRecipientHeaderBytes+100, strings.Repeat("x", maxRecipientHeaderBytes)),
		"plist outside declared message":      "1\nx\nTo: plist@example.com\n\n<plist/>",
		"invalid length":                      "invalid\nTo: hidden@example.com\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			header, err := readEMLXRecipientHeaders(recipientTestFile(t, data))
			if err == nil || header != nil {
				t.Fatalf("accepted unsafe headers: %v, %v", header, err)
			}
		})
	}
}

func TestRecipientEnrichmentUsesCachedPhysicalOwnerWithoutBody(t *testing.T) {
	c, _, events := localReadFixture(t)
	original := []Message{{ID: "101", Account: "Work", Mailbox: "INBOX", Content: "preserved", ContentSource: "mail"}, {ID: "103"}, {ID: "104"}, {ID: "106", Account: "Work", Mailbox: "邮件 café"}}
	got := c.EnrichMessagesWithRecipients("", "", original[:1])
	// Exercise both per-row scope and the selected scope used by listing callers.
	got = append(got[:1], c.EnrichMessagesWithRecipients("Work", "INBOX", original[1:3])...)
	got = append(got, c.EnrichMessagesWithRecipients("Work", "邮件 café", original[3:])...)
	for i, want := range []string{"li@example.com,cafe@example.com,second@example.com", "partial@example.com", "broken@example.com", "li@example.com,cafe@example.com,second@example.com"} {
		if strings.Join(got[i].ToRecipients, ",") != want || !got[i].RecipientStatus.complete() || got[i].RecipientStatus.To.Source != "disk" {
			t.Fatalf("row %d = %+v", i, got[i])
		}
	}
	if got[0].Content != "preserved" || got[0].ContentSource != "mail" || got[0].BodyDiagnostics != nil || got[0].Mailbox != "INBOX" || original[0].RecipientStatus.To.State != "" {
		t.Fatalf("envelope/body or input mutated: %+v / %+v", got[0], original[0])
	}
	if events.Load() != 0 {
		t.Fatalf("header-only cached read dispatched %d Mail events", events.Load())
	}
}

func recipientEventContains(v ae.Value, want string) bool {
	if v.Code == want {
		return true
	}
	for _, child := range v.Fields {
		if recipientEventContains(child, want) {
			return true
		}
	}
	for _, child := range v.Items {
		if recipientEventContains(child, want) {
			return true
		}
	}
	return false
}

func assertRecipientReadEvents(t *testing.T, f *nativeReadFixture) {
	t.Helper()
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("recipient read dispatched %s", event.ID)
		}
		for _, v := range event.Params {
			if recipientEventContains(v, "pALL") && recipientEventContains(v, "mssg") {
				t.Fatal("recipient read requested all message properties")
			}
			for _, forbidden := range []string{"ctnt", "isrd"} {
				if recipientEventContains(v, forbidden) {
					t.Fatalf("recipient read requested %s", forbidden)
				}
			}
		}
	}
}

func TestRecipientNativeReadChecksMembershipAndHeaders(t *testing.T) {
	f := newNativeReadFixture()
	f.properties["mssg:42"]["alhe"] = ae.String("To: native@example.com\r\nDelivered-To: delivered@example.com\r\nX-Original-To: original@example.com\r\n")
	c := fixtureClient(t, f)
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}})[0]
	if !got.RecipientStatus.complete() || got.RecipientStatus.To.Source != "mail" || !reflect.DeepEqual(got.ToRecipients, []string{"native@example.com"}) || !reflect.DeepEqual(got.DeliveredTo, []string{"delivered@example.com"}) || !reflect.DeepEqual(got.OriginalTo, []string{"original@example.com"}) || got.Content != "" {
		t.Fatalf("native recipients = %+v", got)
	}
	membership, headers := -1, -1
	for i, event := range f.events {
		v := event.Params["----"]
		if recipientEventContains(v, "ID  ") && v.Fields["from"].Fields["form"].Code == "test" && v.Fields["from"].Fields["want"].Code == "mssg" {
			membership = i
		}
		if recipientEventContains(v, "alhe") {
			headers = i
		}
	}
	if membership < 0 || headers <= membership {
		t.Fatalf("membership=%d headers=%d", membership, headers)
	}
	assertRecipientReadEvents(t, f)
}

func TestRecipientFreshReadDiscardsStaleEvidence(t *testing.T) {
	for _, outcome := range []string{"available", "permission", "missing", "foreign mailbox", "foreign account"} {
		t.Run(outcome, func(t *testing.T) {
			f := newNativeReadFixture()
			f.properties["mssg:42"]["alhe"] = ae.String("To: fresh@example.com\r\n")
			c := fixtureClient(t, f)
			account, mailbox, id := "Personal", "INBOX", "42"
			switch outcome {
			case "permission":
				f.fail = func(ae.Event) error { return &ae.Error{Code: -1743, Message: "automation denied"} }
			case "missing":
				id = "999"
			case "foreign mailbox":
				mailbox = "All Mail"
			case "foreign account":
				account = "Other"
			}
			stale := Message{ID: id, ToRecipients: []string{"stale@example.com"}, CcRecipients: []string{"stale@example.com"}, BccRecipients: []string{"stale@example.com"}, DeliveredTo: []string{"stale@example.com"}, OriginalTo: []string{"stale@example.com"}}
			for _, field := range recipientFields(&stale) {
				*field.status = RecipientFieldStatus{State: "available", Source: "disk"}
			}
			got := c.EnrichMessagesWithRecipients(account, mailbox, []Message{stale})[0]
			if outcome == "available" {
				if !got.RecipientStatus.complete() || !reflect.DeepEqual(got.ToRecipients, []string{"fresh@example.com"}) || len(got.DeliveredTo)+len(got.OriginalTo)+len(got.CcRecipients)+len(got.BccRecipients) != 0 {
					t.Fatalf("fresh evidence = %+v", got)
				}
			} else {
				for _, field := range recipientFields(&got) {
					if len(*field.values) != 0 || field.status.State != "unavailable" || field.status.Error == "" {
						t.Fatalf("%s leaked recipient evidence: %+v", outcome, got)
					}
				}
				for _, event := range f.events {
					if recipientEventContains(event.Params["----"], "alhe") {
						t.Fatal("out-of-scope or denied read requested headers")
					}
				}
			}
			if stale.ToRecipients[0] != "stale@example.com" || stale.RecipientStatus.To.Source != "disk" {
				t.Fatal("fresh read mutated original")
			}
			assertRecipientReadEvents(t, f)
		})
	}
}

func TestRecipientNativeOptionalHeadersRetainTypedEvidenceAndUnknownDelivery(t *testing.T) {
	f := newNativeReadFixture()
	delete(f.properties["mssg:42"], "alhe")
	c := fixtureClient(t, f)
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		value, stats, err := f.sender(ctx, event)
		if event.ID == "getd" && recipientEventContains(event.Params["----"], "trcp") && recipientEventContains(event.Params["----"], "radd") {
			return ae.List(ae.String("typed@example.com")), stats, nil
		}
		return value, stats, err
	}
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}})[0]
	if !reflect.DeepEqual(got.ToRecipients, []string{"typed@example.com"}) {
		t.Fatalf("typed recipient values = %v", got.ToRecipients)
	}
	for _, status := range []RecipientFieldStatus{got.RecipientStatus.To, got.RecipientStatus.Cc, got.RecipientStatus.Bcc} {
		if status.State != "available" || status.Source != "mail" {
			t.Fatalf("typed status = %+v", status)
		}
	}
	for _, status := range []RecipientFieldStatus{got.RecipientStatus.DeliveredTo, got.RecipientStatus.OriginalTo} {
		if status.State != "unavailable" || status.Error == "" {
			t.Fatalf("unobserved delivery header = %+v", status)
		}
	}
	for _, class := range []string{"trcp", "crcp", "brcp"} {
		found := false
		for _, event := range f.events {
			found = found || recipientEventContains(event.Params["----"], class)
		}
		if !found {
			t.Fatalf("did not request typed %s", class)
		}
	}
	assertRecipientReadEvents(t, f)
}

func recipientScanMessage(id, address string, available bool) Message {
	m := Message{ID: id, ToRecipients: []string{address}}
	for _, field := range recipientFields(&m) {
		*field.status = RecipientFieldStatus{State: "available", Source: "disk"}
	}
	if !available {
		m.ToRecipients = nil
		m.RecipientStatus.To = RecipientFieldStatus{State: "unavailable", Error: "header inaccessible"}
	}
	return m
}

func TestRecipientScanRejectsReadModeMismatch(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		for _, initialMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("checkpoint=%t/initial=%t", checkpoint, initialMode), func(t *testing.T) {
				req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Limit: 1, WithRecipients: initialMode}
				if checkpoint {
					req.Checkpoint = filepath.Join(t.TempDir(), "scan.json")
				}
				first, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
					return scanPage{Messages: []Message{{ID: "2"}}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 2, ID: 2}}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if !checkpoint {
					req.Cursor = first.NextCursor
				}
				req.WithRecipients = !initialMode
				called := false
				_, err = scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { called = true; return scanPage{}, nil })
				want := ErrInvalidScanCursor
				if checkpoint {
					want = ErrInvalidScanCheckpoint
				}
				if !errors.Is(err, want) || called {
					t.Fatalf("mismatched mode: %v called=%t", err, called)
				}
			})
		}
	}
}

func TestRecipientScanCoverageMergesDuplicateEvidenceAndResumes(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(fmt.Sprintf("checkpoint=%t", checkpoint), func(t *testing.T) {
			req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "All Mail"}}, Limit: 1, WithRecipients: true}
			if checkpoint {
				req.Checkpoint = filepath.Join(t.TempDir(), "scan.json")
			}
			first, err := scanMessages(req, func(scope SearchMailbox, _ int, _ *ScanPosition) (scanPage, error) {
				if scope.Mailbox == "INBOX" {
					return scanPage{Messages: []Message{recipientScanMessage("2", "saved@example.com", true)}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 2, ID: 2}}, nil
				}
				return scanPage{}, errors.New("retry mailbox")
			})
			if err != nil || first.RecipientCoverage == nil || *first.RecipientCoverage != (RecipientCoverage{Inspected: 1, Complete: true}) || first.Complete {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			if !checkpoint {
				req.Cursor = first.NextCursor
			}
			second, err := scanMessages(req, func(scope SearchMailbox, _ int, after *ScanPosition) (scanPage, error) {
				if scope.Mailbox == "INBOX" {
					if after == nil || after.ID != 2 {
						t.Fatalf("resume position=%+v", after)
					}
					return scanPage{Messages: []Message{recipientScanMessage("1", "new@example.com", true)}}, nil
				}
				return scanPage{Messages: []Message{recipientScanMessage("2", "", false), recipientScanMessage("1", "", false)}}, nil
			})
			want := RecipientCoverage{Inspected: 2, Unavailable: 1, Complete: false}
			if checkpoint {
				want = RecipientCoverage{Inspected: 2, Complete: true}
			}
			if err != nil || !second.Complete || second.RecipientCoverage == nil || *second.RecipientCoverage != want {
				t.Fatalf("resumed coverage=%+v result=%+v err=%v", second.RecipientCoverage, second, err)
			}
			for _, m := range second.Messages {
				if m.ID == "1" && (!reflect.DeepEqual(m.ToRecipients, []string{"new@example.com"}) || m.RecipientStatus.To.State != "available" || len(m.Mailboxes) != 2) {
					t.Fatalf("duplicate erased good evidence: %+v", m)
				}
				if checkpoint && m.ID == "2" && (!reflect.DeepEqual(m.ToRecipients, []string{"saved@example.com"}) || len(m.Mailboxes) != 2) {
					t.Fatalf("checkpoint erased prior evidence: %+v", m)
				}
			}
		})
	}
}

func TestRecipientDiskOwnershipRejectsForeignAccountFile(t *testing.T) {
	c, root, _ := localReadFixture(t)
	f := localReadLiveFixture(c)
	writeEMLX(t, filepath.Join(root, "OTHER", "INBOX.mbox", "UUID", "Data", "Messages", "102.emlx"), "To: foreign@example.com\n\nbody")
	// The index includes a label in Work's INBOX for this Other-owned row.
	got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "102"}})[0]
	for _, field := range recipientFields(&got) {
		if len(*field.values) != 0 || field.status.State != "unavailable" {
			t.Fatalf("foreign physical owner leaked evidence: %+v", got)
		}
	}
	if len(f.events) == 0 {
		t.Fatal("foreign cached ownership was accepted without scoped Mail fallback")
	}
	assertRecipientReadEvents(t, f)
}

func TestRecipientNativeMalformedTypedToPreservesReadableCcAndBcc(t *testing.T) {
	f := newNativeReadFixture()
	delete(f.properties["mssg:42"], "alhe")
	c := fixtureClient(t, f)
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		value, stats, err := f.sender(ctx, event)
		v := event.Params["----"]
		if event.ID == "getd" && recipientEventContains(v, "radd") {
			if recipientEventContains(v, "trcp") {
				return ae.List(ae.Int(7)), stats, nil
			}
			if recipientEventContains(v, "crcp") {
				return ae.List(ae.String("cc@example.com")), stats, nil
			}
			if recipientEventContains(v, "brcp") {
				return ae.List(ae.String("bcc@example.com")), stats, nil
			}
		}
		return value, stats, err
	}
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}})[0]
	if got.RecipientStatus.To.State != "unavailable" || !strings.Contains(got.RecipientStatus.To.Error, "expected text") || len(got.ToRecipients) != 0 || !reflect.DeepEqual(got.CcRecipients, []string{"cc@example.com"}) || !reflect.DeepEqual(got.BccRecipients, []string{"bcc@example.com"}) || got.RecipientStatus.Cc.State != "available" || got.RecipientStatus.Bcc.State != "available" {
		t.Fatalf("malformed To erased readable recipients: %+v", got)
	}
	assertRecipientReadEvents(t, f)
}
