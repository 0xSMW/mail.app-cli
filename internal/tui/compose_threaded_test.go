package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestComposeRetainsOriginalIdentityAfterEditorAndSourceChanges(t *testing.T) {
	original := &mail.Message{Account: "Work", Mailbox: "INBOX", ID: "42", Sender: "sender@example.test", Subject: "Original", Content: "Quoted body"}
	c := newComposeModal(composeReplyAll, "Work", original, nil)
	*original = mail.Message{Account: "Other", Mailbox: "Archive", ID: "99"}
	c.inputs[fieldTo].SetValue("edited@example.test")
	c.inputs[fieldSubject].SetValue("Edited subject")
	c.body.SetValue("Edited body")
	if c.originalAccount != "Work" || c.originalMailbox != "INBOX" || c.originalID != "42" {
		t.Fatalf("editor lost original identity: %+v", c)
	}
}

func TestNativeComposeReplyAndForwardRequireOriginalIdentity(t *testing.T) {
	for _, mode := range []composeMode{composeReply, composeReplyAll, composeForward} {
		for _, draft := range []bool{false, true} {
			m := loadedModel(t)
			if err := m.client.SetBackend(mail.BackendNative); err != nil {
				t.Fatal(err)
			}
			// Cancel transport too: a routing regression cannot send a real email.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			m.writeCtx = ctx
			c := newComposeModal(mode, "Work", &mail.Message{Account: "Work", Mailbox: "INBOX", Sender: "sender@example.test"}, nil)
			c.inputs[fieldTo].SetValue("edited@example.test")
			c.inputs[fieldSubject].SetValue("Edited subject")
			c.body.SetValue("Edited body")
			command := c.submit(&m, draft)
			if command == nil {
				t.Fatal("submit did not queue a write")
			}
			done := command().(writeDoneMsg).inner.(composeDoneMsg)
			if done.err == nil || !strings.Contains(done.err.Error(), "invalid original message ID") {
				t.Fatalf("mode %d draft %v did not use threaded dispatch: %v", mode, draft, done.err)
			}
		}
	}
}

func TestLegacyComposeReplyKeepsExistingDispatch(t *testing.T) {
	m := loadedModel(t)
	if err := m.client.SetBackend(mail.BackendLegacy); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.writeCtx = ctx
	c := newComposeModal(composeReply, "Work", &mail.Message{Sender: "sender@example.test"}, nil)
	c.inputs[fieldSubject].SetValue("Edited subject")
	done := c.submit(&m, false)().(writeDoneMsg).inner.(composeDoneMsg)
	if done.err == nil || strings.Contains(done.err.Error(), "original message") {
		t.Fatalf("legacy dispatch unexpectedly required original identity: %v", done.err)
	}
}

func TestRestrictedTUIComposeRetainsTextAfterBlockedSend(t *testing.T) {
	for _, backend := range []string{mail.BackendNative, mail.BackendLegacy} {
		for _, mode := range []composeMode{composeNew, composeReply, composeReplyAll, composeForward} {
			m := loadedModel(t)
			m.client = m.client.WithSendingDisabled()
			if err := m.client.SetBackend(backend); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			m.writeCtx = ctx
			c := newComposeModal(mode, "Work", &mail.Message{Account: "Work", Mailbox: "INBOX", ID: "42", Sender: "sender@example.test"}, nil)
			c.inputs[fieldTo].SetValue("to@example.test")
			c.inputs[fieldSubject].SetValue("Review")
			c.body.SetValue("Keep this text")
			m.modal = c
			done := c.submit(&m, false)().(writeDoneMsg).inner.(composeDoneMsg)
			if done.err != mail.ErrSendingDisabled {
				t.Fatalf("%s/%d: %v", backend, mode, done.err)
			}
			next, _ := m.onComposeDone(done)
			kept, ok := next.(model).modal.(*composeModal)
			if !ok || kept.body.Value() != "Keep this text" || kept.sending || !strings.Contains(kept.status, "email sending is disabled") {
				t.Fatalf("editor lost after refusal: %+v", kept)
			}
		}
	}
}
