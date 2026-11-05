package mail

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func addRecipientFixtureMessage(f *nativeReadFixture, id int64, mailbox string) {
	key := "mssg:" + strconv.FormatInt(id, 10)
	f.properties[key] = make(map[string]ae.Value)
	for property, value := range f.properties["mssg:42"] {
		f.properties[key][property] = value
	}
	f.properties[key]["ID  "] = ae.Int(id)
	f.messages[mailbox] = append(f.messages[mailbox], fixtureRef("mssg", strconv.FormatInt(id, 10)))
}

func TestRecipientReferenceCacheIsInvocationScopedAndChecksEveryMembership(t *testing.T) {
	f := newNativeReadFixture()
	addRecipientFixtureMessage(f, 43, "mbxp:inbox")
	addRecipientFixtureMessage(f, 44, "mbxp:archive")
	c := fixtureClient(t, f)
	c.shared.accounts = []Account{{ID: "a", Name: "Personal"}}
	c.shared.accountsLoaded = true
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}, {ID: "43"}, {ID: "44"}})
	if !got[0].RecipientStatus.complete() || !got[1].RecipientStatus.complete() || got[2].RecipientStatus.complete() {
		t.Fatalf("recipient membership results = %+v", got)
	}
	accountQueries, mailboxQueries, memberships, headers := 0, 0, 0, 0
	countEvents := func() {
		accountQueries, mailboxQueries, memberships, headers = 0, 0, 0, 0
		for _, event := range f.events {
			v := event.Params["----"]
			if v.Fields["want"].Code == "mact" && v.Fields["form"].Code == "test" {
				accountQueries++
			}
			if reflect.DeepEqual(v, ae.Property("pnam", ae.All("mbxp", fixtureRef("mact", "a")))) {
				mailboxQueries++
			}
			if v.Fields["seld"].Code == "ID  " && v.Fields["from"].Fields["want"].Code == "mssg" && v.Fields["from"].Fields["form"].Code == "test" {
				memberships++
			}
			if v.Fields["seld"].Code == "alhe" {
				headers++
			}
		}
	}
	countEvents()
	if accountQueries != 1 || mailboxQueries != 1 || memberships != 3 || headers != 2 {
		t.Fatalf("account=%d mailbox=%d membership=%d headers=%d", accountQueries, mailboxQueries, memberships, headers)
	}
	c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}})
	countEvents()
	if accountQueries != 2 || mailboxQueries != 2 {
		t.Fatalf("references escaped invocation: account=%d mailbox=%d", accountQueries, mailboxQueries)
	}
	assertRecipientReadEvents(t, f)
}

func TestRecipientTimeoutReportsPendingOperationAndPreservesEvidence(t *testing.T) {
	for _, phase := range []string{"recipient_account", "recipient_mailbox", "recipient_membership", "recipient_headers", "recipient_to", "recipient_cc", "recipient_bcc"} {
		t.Run(phase, func(t *testing.T) {
			f := newNativeReadFixture()
			if phase == "recipient_to" || phase == "recipient_cc" || phase == "recipient_bcc" {
				delete(f.properties["mssg:42"], "alhe")
			}
			f.fail = func(event ae.Event) error {
				v := event.Params["----"]
				matches := false
				switch phase {
				case "recipient_account":
					matches = v.Fields["want"].Code == "mact"
				case "recipient_mailbox":
					matches = recipientEventContains(v, "mbxp")
				case "recipient_membership":
					matches = recipientEventContains(v, "mssg")
				case "recipient_headers":
					matches = v.Fields["seld"].Code == "alhe"
				default:
					class := map[string]string{"recipient_to": "trcp", "recipient_cc": "crcp", "recipient_bcc": "brcp"}[phase]
					matches = v.Fields["seld"].Code == "radd" && recipientEventContains(v, class)
				}
				if matches {
					return context.DeadlineExceeded
				}
				return nil
			}
			c := fixtureClient(t, f)
			m := Message{ID: "42", DeliveredTo: []string{"saved@example.com"}, RecipientStatus: RecipientStatus{DeliveredTo: RecipientFieldStatus{State: "available", Source: "disk"}}}
			err := c.withNativeAutomation(time.Second, func(n *nativeMail) error {
				return n.readMessageRecipientsWithCache("Personal", "INBOX", &m, newRecipientReferenceCache())
			})
			var timeout *AutomationTimeoutError
			if !errors.As(err, &timeout) || timeout.Trace.Pending != phase || timeout.Trace.LastCompleted == "" {
				t.Fatalf("timeout trace = %+v, error = %v", timeout, err)
			}
			unavailableRecipients(&m, err)
			if !reflect.DeepEqual(m.DeliveredTo, []string{"saved@example.com"}) || m.RecipientStatus.DeliveredTo.Source != "disk" {
				t.Fatalf("timeout erased observed evidence: %+v", m)
			}
			assertRecipientReadEvents(t, f)
		})
	}
}

func TestRecipientBatchTimeoutDistinguishesActiveAndUnattemptedRows(t *testing.T) {
	f := newNativeReadFixture()
	addRecipientFixtureMessage(f, 43, "mbxp:inbox")
	f.fail = func(event ae.Event) error {
		if event.Params["----"].Fields["seld"].Code == "alhe" {
			return context.DeadlineExceeded
		}
		return nil
	}
	c := fixtureClient(t, f)
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}, {ID: "43"}})
	for _, field := range recipientFields(&got[0]) {
		if !strings.Contains(field.status.Error, "during recipient_headers") || strings.Contains(field.status.Error, "not attempted") {
			t.Fatalf("active row lost timeout phase: %+v", *field.status)
		}
	}
	for _, field := range recipientFields(&got[1]) {
		if !strings.Contains(field.status.Error, "recipient lookup not attempted because shared batch stopped") || !strings.Contains(field.status.Error, "during recipient_headers") {
			t.Fatalf("unattempted row claims its own timeout: %+v", *field.status)
		}
	}
	assertRecipientReadEvents(t, f)
}

func TestRecipientTimeoutRetainsEarlierPartialAndUnavailableErrors(t *testing.T) {
	f := newNativeReadFixture()
	f.fail = func(event ae.Event) error {
		if event.Params["----"].Fields["seld"].Code == "alhe" {
			return context.DeadlineExceeded
		}
		return nil
	}
	c := fixtureClient(t, f)
	m := Message{ID: "42", ToRecipients: []string{"saved@example.com"}, RecipientStatus: RecipientStatus{
		To: RecipientFieldStatus{State: "partial", Source: "disk", Error: "malformed disk To"},
		Cc: RecipientFieldStatus{State: "unavailable", Error: "malformed disk Cc"},
	}}
	err := c.withNativeAutomation(time.Second, func(n *nativeMail) error {
		return n.readMessageRecipients("Personal", "INBOX", &m)
	})
	unavailableRecipients(&m, err)
	if m.RecipientStatus.To.State != "partial" || m.RecipientStatus.To.Error != "malformed disk To" || !reflect.DeepEqual(m.ToRecipients, []string{"saved@example.com"}) || m.RecipientStatus.Cc.Error != "malformed disk Cc" {
		t.Fatalf("timeout replaced earlier recipient evidence: %+v", m)
	}
	if !strings.Contains(m.RecipientStatus.Bcc.Error, "during recipient_headers") {
		t.Fatalf("newly unavailable field lacks phase: %+v", m.RecipientStatus.Bcc)
	}
}

func TestRecipientBatchTimeoutAddsDispositionToMalformedDiskHeaders(t *testing.T) {
	c, root, _ := localReadFixture(t)
	f := localReadLiveFixture(c)
	addRecipientFixtureMessage(f, 101, "mbxp:inbox")
	addRecipientFixtureMessage(f, 103, "mbxp:inbox")
	owner := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages")
	writeEMLX(t, filepath.Join(owner, "101.emlx"), "To: invalid address\n\nbody")
	writeEMLX(t, filepath.Join(owner, "103.partial.emlx"), "To: invalid address\n\nbody")
	f.fail = func(event ae.Event) error {
		if event.Params["----"].Fields["seld"].Code == "alhe" {
			return context.DeadlineExceeded
		}
		return nil
	}
	got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "101"}, {ID: "103"}})
	for i, m := range got {
		if !strings.Contains(m.RecipientStatus.To.Error, "parse To recipients") || !strings.Contains(m.RecipientStatus.To.Error, "during recipient_headers") {
			t.Fatalf("row %d lost disk or batch cause: %+v", i, m.RecipientStatus.To)
		}
		if strings.Contains(m.RecipientStatus.To.Error, "not attempted") != (i == 1) {
			t.Fatalf("row %d has wrong batch disposition: %+v", i, m.RecipientStatus.To)
		}
		if m.RecipientStatus.Cc.State != "available" || m.RecipientStatus.Cc.Source != "disk" {
			t.Fatalf("row %d lost available disk field: %+v", i, m.RecipientStatus.Cc)
		}
	}
	assertRecipientReadEvents(t, f)
}

func TestRecipientBatchTimeoutDoesNotRelabelCompletedRow(t *testing.T) {
	f := newNativeReadFixture()
	f.properties["mssg:42"]["alhe"] = ae.String("")
	addRecipientFixtureMessage(f, 43, "mbxp:inbox")
	addRecipientFixtureMessage(f, 44, "mbxp:inbox")
	f.fail = func(event ae.Event) error {
		v := event.Params["----"]
		if v.Fields["seld"].Code == "alhe" && v.Fields["from"].Fields["seld"].Number == 43 {
			return context.DeadlineExceeded
		}
		return nil
	}
	c := fixtureClient(t, f)
	got := c.EnrichMessagesWithRecipients("Personal", "INBOX", []Message{{ID: "42"}, {ID: "43"}, {ID: "44"}})
	for _, field := range recipientFields(&got[0])[3:] {
		if field.status.State != "unavailable" || strings.Contains(field.status.Error, "timed out") || strings.Contains(field.status.Error, "batch stopped") {
			t.Fatalf("completed row inherited later row's failure: %+v", *field.status)
		}
	}
	if !strings.Contains(got[1].RecipientStatus.To.Error, "during recipient_headers") || !strings.Contains(got[2].RecipientStatus.To.Error, "not attempted") {
		t.Fatalf("active and queued dispositions missing: %+v", got)
	}
}
