package mail

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestRecipientHeaderRecoveryAccumulatesPartialObservations(t *testing.T) {
	for name, observe := range map[string]func(*Message){
		"typed collection": func(message *Message) {
			populateMailRecipientCollection(recipientFields(message)[0], []string{"other@example.test", "me"})
		},
		"repeated headers": func(message *Message) {
			populateRecipientHeaders(message, netmail.Header{"To": {"other@example.test", "me"}}, "mail")
		},
		"malformed header prefix": func(message *Message) {
			populateRecipientHeaderPrefix(message, netmail.Header{"To": {"other@example.test"}}, "mail", errors.New("incomplete header block"))
		},
		"merge": func(message *Message) {
			mergeRecipientEvidence(message, Message{ToRecipients: []string{"other@example.test"}, RecipientStatus: RecipientStatus{To: RecipientFieldStatus{State: "partial", Source: "mail", Error: "incomplete collection"}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			message := Message{ToRecipients: []string{"known@example.test"}, RecipientStatus: RecipientStatus{To: RecipientFieldStatus{State: "partial", Source: "disk", Error: "incomplete header block"}}}
			populateMailRecipientCollection(recipientFields(&message)[0], []string{"me"})
			if !reflect.DeepEqual(message.ToRecipients, []string{"known@example.test"}) || message.RecipientStatus.To.State != "partial" || message.RecipientStatus.To.Source != "disk" {
				t.Fatalf("failure-only observation changed provenance: To=%v status=%+v", message.ToRecipients, message.RecipientStatus.To)
			}
			observe(&message)
			assertEvidence := func() {
				t.Helper()
				if !reflect.DeepEqual(message.ToRecipients, []string{"known@example.test", "other@example.test"}) || message.RecipientStatus.To.State != "partial" || message.RecipientStatus.To.Source != "mixed" {
					t.Fatalf("accumulated To=%v status=%+v", message.ToRecipients, message.RecipientStatus.To)
				}
				for _, target := range []string{"known@example.test", "other@example.test"} {
					if matched, known := MatchRecipient(message, target); !matched || !known {
						t.Fatalf("target=%s match=%t known=%t", target, matched, known)
					}
				}
				if matched, known := MatchRecipient(message, "absent@example.test"); matched || known {
					t.Fatalf("partial negative match=%t known=%t", matched, known)
				}
			}
			assertEvidence()
			populateMailRecipientCollection(recipientFields(&message)[0], []string{"me"})
			assertEvidence()
			populateRecipientHeaders(&message, netmail.Header{"To": {"broken <"}}, "mail")
			assertEvidence()
			unavailableRecipients(&message, errors.New("later unavailable observation"))
			assertEvidence()
			mergeRecipientEvidence(&message, Message{RecipientStatus: RecipientStatus{To: RecipientFieldStatus{State: "unavailable", Error: "later merge unavailable"}}})
			assertEvidence()
			populateRecipientHeaderPrefix(&message, netmail.Header{"Subject": {"fixture"}}, "mail", errors.New("later prefix has no To"))
			assertEvidence()

			// A fully inspected collection is authoritative and can replace partial evidence.
			populateMailRecipientCollection(recipientFields(&message)[0], []string{"complete@example.test"})
			if !reflect.DeepEqual(message.ToRecipients, []string{"complete@example.test"}) || message.RecipientStatus.To.State != "available" {
				t.Fatalf("complete collection To=%v status=%+v", message.ToRecipients, message.RecipientStatus.To)
			}
		})
	}
}

func TestRecipientHeaderRecoverySingleExplicitMailbox(t *testing.T) {
	for name, value := range map[string]string{
		"empty encoded name":      `=?utf-8?Q??= <target@example.test>`,
		"malformed quoted name":   `"\""" <target@example.test>`,
		"mailbox as display name": `target@example.test <target@example.test>`,
	} {
		t.Run(name, func(t *testing.T) {
			var message Message
			populateRecipientHeaders(&message, netmail.Header{"To": {value}}, "disk")
			if !reflect.DeepEqual(message.ToRecipients, []string{"target@example.test"}) || message.RecipientStatus.To.State != "available" || message.RecipientStatus.To.Source != "disk" {
				t.Fatalf("recovered To=%v status=%+v", message.ToRecipients, message.RecipientStatus.To)
			}
			if matched, known := MatchRecipient(message, "target@example.test"); !matched || !known {
				t.Fatalf("recovered mailbox match=%t known=%t", matched, known)
			}
		})
	}
}

func TestRecipientHeaderRecoveryRejectsAmbiguousOrUnvalidatedValues(t *testing.T) {
	for name, value := range map[string]string{
		"different mailbox display name": `other@example.test <target@example.test>`,
		"unclosed angle":                 `Display <target@example.test`,
		"short local name":               `me`,
		"wrapped group":                  `<Undisclosed-Recipient:;>`,
		"trailing text":                  `=?utf-8?Q??= <target@example.test> trailing`,
		"multiple explicit mailboxes":    `=?utf-8?Q??= <target@example.test> <other@example.test>`,
		"address in malformed text":      `Please reach target@example.test tomorrow <`,
	} {
		t.Run(name, func(t *testing.T) {
			var message Message
			populateRecipientHeaders(&message, netmail.Header{"To": {value}}, "disk")
			if len(message.ToRecipients) != 0 || message.RecipientStatus.To.State != "unavailable" {
				t.Fatalf("unsafe recovery To=%v status=%+v", message.ToRecipients, message.RecipientStatus.To)
			}
			if matched, known := MatchRecipient(message, "target@example.test"); matched || known {
				t.Fatalf("ambiguous mailbox match=%t known=%t", matched, known)
			}
		})
	}
}

func TestRecipientHeaderRecoveryRepeatedEntriesKeepPartialEvidence(t *testing.T) {
	var message Message
	populateRecipientHeaders(&message, netmail.Header{
		"To":           {"known@example.test", "me"},
		"Delivered-To": {"actual@example.test", "Autoresponder"},
	}, "disk")
	for _, field := range []recipientField{recipientFields(&message)[0], recipientFields(&message)[3]} {
		if field.status.State != "partial" || field.status.Source != "disk" || field.status.Error == "" || len(*field.values) != 1 {
			t.Fatalf("repeated %s values=%v status=%+v", field.name, *field.values, field.status)
		}
	}
	if !reflect.DeepEqual(message.ToRecipients, []string{"known@example.test"}) || !reflect.DeepEqual(message.DeliveredTo, []string{"actual@example.test"}) || message.RecipientStatus.complete() {
		t.Fatalf("partial evidence=%+v", message)
	}
	if matched, known := MatchRecipient(message, "known@example.test"); !matched || !known {
		t.Fatalf("known partial positive match=%t known=%t", matched, known)
	}
	for _, target := range []string{"absent@example.test", "actual@example.test"} {
		if matched, known := MatchRecipient(message, target); matched || known {
			t.Fatalf("partial negative/delivery target=%s match=%t known=%t", target, matched, known)
		}
	}
}

func TestRecipientHeaderRecoveryStopsBeforeMalformedSourceContent(t *testing.T) {
	for name, malformed := range map[string]string{"body": "Private body content", "MIME boundary": "--private-mime-boundary"} {
		t.Run(name, func(t *testing.T) {
			source := "Cc: known@example.test\n" + malformed + "\nTo: body-only@example.test\nDelivered-To: body-delivery@example.test\n\n"
			header, err := readEMLXRecipientHeaders(recipientTestFile(t, fmt.Sprintf("%d\n%s", len(source), source)))
			if err == nil || header == nil || !reflect.DeepEqual(header["Cc"], []string{"known@example.test"}) {
				t.Fatalf("prefix=%v err=%v", header, err)
			}
			if len(header["To"])+len(header["Delivered-To"]) != 0 {
				t.Fatalf("body/MIME content parsed as headers: %v", header)
			}
			for _, private := range []string{malformed, "body-only@example.test", "body-delivery@example.test"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("header diagnostic exposes private content: %v", err)
				}
			}
		})
	}
}

func TestRecipientHeaderRecoveryNativeRejectsInvalidTypedMailbox(t *testing.T) {
	for _, invalid := range []string{"me", "Undisclosed-Recipient:;"} {
		t.Run(invalid, func(t *testing.T) {
			fixture := newNativeReadFixture()
			delete(fixture.properties["mssg:42"], "alhe")
			client := fixtureClient(t, fixture)
			client.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				value, stats, err := fixture.sender(ctx, event)
				if recipientEventContains(event.Params["----"], "trcp") && recipientEventContains(event.Params["----"], "radd") {
					return ae.List(ae.String(invalid)), stats, nil
				}
				return value, stats, err
			}
			got := client.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}})[0]
			if len(got.ToRecipients) != 0 || got.RecipientStatus.To.State != "unavailable" || got.RecipientStatus.To.Error == "" {
				t.Fatalf("invalid typed mailbox accepted: %+v", got)
			}
			assertRecipientReadEvents(t, fixture)
		})
	}
}

func TestRecipientHeaderRecoveryCachedPrefixSurvivesMailFailure(t *testing.T) {
	for _, code := range []int{-1743, -1712} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			client, root, events := localReadFixture(t)
			path := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages", "101.emlx")
			writeEMLX(t, path, "Cc: known@example.test\nPrivate body content\nTo: body-only@example.test\n\n")
			client.shared.nativeSender = func(context.Context, ae.Event) (ae.Value, ae.Stats, error) {
				events.Add(1)
				return ae.Value{}, ae.Stats{}, &ae.Error{Code: code, Message: "fixture fallback denied"}
			}
			got := client.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "101"}})[0]
			if !reflect.DeepEqual(got.CcRecipients, []string{"known@example.test"}) || got.RecipientStatus.Cc.State != "partial" || got.RecipientStatus.Cc.Source != "disk" {
				t.Fatalf("cached prefix lost: %+v", got)
			}
			if len(got.ToRecipients) != 0 || got.RecipientStatus.To.State != "unavailable" || got.RecipientStatus.complete() || events.Load() == 0 {
				t.Fatalf("incomplete prefix/fallback=%+v events=%d", got, events.Load())
			}
			if matched, known := MatchRecipient(got, "known@example.test"); !matched || !known {
				t.Fatalf("recovered prefix positive match=%t known=%t", matched, known)
			}
			if matched, known := MatchRecipient(got, "body-only@example.test"); matched || known {
				t.Fatalf("unobserved To match=%t known=%t", matched, known)
			}
		})
	}
}
