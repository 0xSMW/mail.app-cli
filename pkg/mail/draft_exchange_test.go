package mail

import (
	"context"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestDraftSenderAddressExchangeDuplicateDisplay(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"sender@example.test", "sender@example.test"},
		{"Sender Name <sender@example.test>", "sender@example.test"},
		{"\"sender@example.test\" <sender@example.test>", "sender@example.test"},
		{"sender@example.test <Sender@Example.Test>", "Sender@Example.Test"},
		{"  sender@example.test <Sender@Example.Test>  ", "Sender@Example.Test"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := draftSenderAddress(tc.value)
			if err != nil || got != tc.want {
				t.Fatalf("sender=%q err=%v; want %q", got, err, tc.want)
			}
		})
	}
	for _, value := range []string{
		"", "sender@example.test <other@example.test>",
		"sender@example.test <sender@example.test> trailing",
		"sender@example.test <<sender@example.test>>",
		"sender@example.test <sender@example.test>, other@example.test",
		"sender@example.test (comment) <sender@example.test>",
		"sender@example.test <Name <sender@example.test>>",
		"sender@example.test\r\n <sender@example.test>",
		"sender@example.test\nBcc: other@example.test",
	} {
		t.Run("reject "+value, func(t *testing.T) {
			if got, err := draftSenderAddress(value); err == nil {
				t.Fatalf("accepted sender %q as %q", value, got)
			}
		})
	}
}

func TestDraftReconstructionUsesExchangeSenderNormalization(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	snapshot.Message.Sender = "alias@example.test <Alias@Example.Test>"
	snapshot.Source = strings.Replace(snapshot.Source, "From: Person <alias@example.test>", "From: alias@example.test <Alias@Example.Test>", 1)
	if body, _, err := reconstructibleDraft(snapshot); err != nil || body != snapshot.Message.Content {
		t.Fatalf("body=%q err=%v", body, err)
	}
	snapshot.Source = strings.Replace(snapshot.Source, "From: alias@example.test <Alias@Example.Test>", "From: other@example.test", 1)
	if _, _, err := reconstructibleDraft(snapshot); err == nil {
		t.Fatal("accepted a source sender different from the Mail sender")
	}
}

func TestDraftSourceRecipientsFillOnlyMissingCollections(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	snapshot.Message.ToRecipients = nil
	snapshot.Message.CcRecipients = []string{}
	if err := reconcileDraftSourceRecipients(snapshot.Message, snapshot.Source); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Message.ToRecipients, []string{"to@example.test"}) || !reflect.DeepEqual(snapshot.Message.CcRecipients, []string{"cc@example.test"}) || !reflect.DeepEqual(snapshot.Message.BccRecipients, []string{"hidden@example.test"}) {
		t.Fatalf("recipient reconciliation changed stored identities: %+v", snapshot.Message)
	}
	if _, _, err := reconstructibleDraft(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Message.ToRecipients = []string{"different@example.test"}
	if err := reconcileDraftSourceRecipients(snapshot.Message, snapshot.Source); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Message.ToRecipients, []string{"different@example.test"}) {
		t.Fatal("contradictory Mail recipient was overwritten")
	}
	if _, _, err := reconstructibleDraft(snapshot); err == nil {
		t.Fatal("contradictory Mail and source recipients were accepted")
	}
}

func TestDraftSourceRecipientsParseNamesGroupsAndBcc(t *testing.T) {
	message := &Message{}
	source := "To: Named <one@example.test>, Team: two@example.test, three@example.test;\r\nCc: \"Other Person\" <cc@example.test>\r\nBcc: hidden@example.test\r\n\r\nbody"
	if err := reconcileDraftSourceRecipients(message, source); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(message.ToRecipients, []string{"one@example.test", "two@example.test", "three@example.test"}) || !reflect.DeepEqual(message.CcRecipients, []string{"cc@example.test"}) || !reflect.DeepEqual(message.BccRecipients, []string{"hidden@example.test"}) {
		t.Fatalf("unexpected recipients %+v", message)
	}
}

func TestDraftSourceRecipientsRejectMalformedOrAmbiguousHeaders(t *testing.T) {
	for _, source := range []string{
		"invalid message source",
		"To: invalid address\r\n\r\nbody",
		"Cc: invalid address\r\n\r\nbody",
		"Bcc: invalid address\r\n\r\nbody",
		"To: one@example.test\r\nTo: two@example.test\r\n\r\nbody",
		"Cc: one@example.test\r\nCc: two@example.test\r\n\r\nbody",
		"Bcc: one@example.test\r\nBcc: two@example.test\r\n\r\nbody",
	} {
		t.Run(source, func(t *testing.T) {
			if err := reconcileDraftSourceRecipients(&Message{}, source); err == nil {
				t.Fatal("accepted malformed or duplicate recipient headers")
			}
		})
	}
	for _, source := range []string{"", "\r\n", "To: \r\nBcc: \r\n\r\nbody"} {
		message := &Message{BccRecipients: []string{"hidden@example.test"}}
		if err := reconcileDraftSourceRecipients(message, source); err != nil || !reflect.DeepEqual(message.BccRecipients, []string{"hidden@example.test"}) {
			t.Fatalf("absent source recipients changed stored Bcc: %+v err=%v", message, err)
		}
	}
}

func TestNativeExchangeDraftSnapshotAndSendUseStoredRecipients(t *testing.T) {
	f := newNativePlainDraftSendFixture()
	f.draft.source = ae.String(strings.Replace(f.draft.source.Text, "From: alias@example.com", "From: alias@example.com <Alias@Example.Com>", 1))
	c := f.client()
	isOriginal := func(ref ae.Value) bool {
		return descriptorClass(ref) == "mssg" && ref.Fields["form"].Code == "ID  " && ref.Fields["seld"].Number == 15 && ref.Fields["from"].Text == "draftbox"
	}
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		ref := event.Params["----"]
		if event.Class+event.ID == "coregetd" {
			if descriptorProperty(ref) == "sndr" && isOriginal(ref.Fields["from"]) {
				return ae.String("alias@example.com <Alias@Example.Com>"), ae.Stats{Events: 1, Attempted: true}, nil
			}
			if descriptorProperty(ref) == "radd" && isOriginal(ref.Fields["from"].Fields["from"]) {
				return ae.List(), ae.Stats{Events: 1, Attempted: true}, nil
			}
		}
		value, stats, err := f.send(ctx, event)
		if err == nil && event.Class+event.ID == "coregetd" && ref.Kind == "list" && value.Kind == "list" {
			for i, item := range ref.Items {
				if descriptorProperty(item) == "sndr" && isOriginal(item.Fields["from"]) {
					value.Items[i] = ae.String("alias@example.com <Alias@Example.Com>")
				}
			}
		}
		return value, stats, err
	}
	draft, err := c.GetDraft("Personal", "15")
	if err != nil || draft.Sender != "alias@example.com <Alias@Example.Com>" || !reflect.DeepEqual(draft.ToRecipients, []string{"to@example.com"}) {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	result, err := c.SendDraftResult("Personal", "15")
	if err != nil || !result.Submitted || result.CleanupPending || f.sendCalls != 1 || f.originalDeletes != 1 || f.outgoing.draftDeletes != 1 || !reflect.DeepEqual(f.outgoing.recipients["trcp"], []string{"to@example.com"}) {
		t.Fatalf("result=%+v err=%v sends=%d deletes=%d outgoing recipients=%+v", result, err, f.sendCalls, f.originalDeletes, f.outgoing.recipients)
	}
}
