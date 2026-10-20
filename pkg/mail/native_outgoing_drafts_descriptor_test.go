package mail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type nativeDraftDescriptorFixture struct {
	events             []ae.Event
	source             ae.Value
	changed            bool
	removed            bool
	deleted            bool
	deleteMarksDeleted bool
	deleteStatus       ae.Value
	subject            string
}

func newNativeDraftDescriptorFixture() *nativeDraftDescriptorFixture {
	return &nativeDraftDescriptorFixture{subject: "Hello", source: ae.String("From: alias@example.com\r\nTo: to@example.com\r\nSubject: Hello\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nWorld 世界\n")}
}
func (f *nativeDraftDescriptorFixture) get(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, ref := range v.Items {
			value, e := f.get(ref)
			if e != nil {
				return ae.Value{}, e
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	class, prop, container := descriptorClass(v), descriptorProperty(v), v.Fields["from"]
	if class == "mact" {
		return ae.List(ruleFixtureRef("account")), nil
	}
	if class == "mbxp" {
		return ae.List(ruleFixtureRef("draftbox")), nil
	}
	if prop == "radd" {
		if descriptorClass(container) == "trcp" {
			return stringsDescriptors([]string{"to@example.com"}), nil
		}
		return ae.List(), nil
	}
	if prop != "" {
		switch container.Text {
		case "account":
			switch prop {
			case "pnam":
				return ae.String("Personal"), nil
			case "ID  ":
				return ae.String("account-id"), nil
			}
		case "draftbox":
			switch prop {
			case "pnam":
				return ae.String("Drafts"), nil
			case "mact":
				return ruleFixtureRef("account"), nil
			}
		}
		if prop == "drmb" {
			return ruleFixtureRef("draftroot"), nil
		}
		if descriptorClass(container) == "mssg" {
			if f.removed {
				return ae.Value{}, &ae.Error{Code: -1728, Message: "missing"}
			}
			switch prop {
			case "isdl":
				if f.deleteStatus.Kind != "" {
					return f.deleteStatus, nil
				}
				return ae.Bool(f.deleted), nil
			case "ID  ":
				return ae.Int(15), nil
			case "subj":
				return ae.String(f.subject), nil
			case "sndr":
				return ae.String("alias@example.com"), nil
			case "ctnt":
				if f.changed {
					return ae.String("changed by human"), nil
				}
				return ae.String("World 世界\n"), nil
			case "mbxp":
				return ruleFixtureRef("draftbox"), nil
			case "raso":
				return f.source, nil
			}
		}
	}
	return ae.Value{}, fmt.Errorf("unexpected draft read %+v", v)
}
func (f *nativeDraftDescriptorFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, event)
	stats := ae.Stats{Events: 1, Attempted: true}
	switch event.Class + event.ID {
	case "coregetd":
		v, e := f.get(event.Params["----"])
		return v, stats, e
	case "coredelo":
		if f.deleteMarksDeleted {
			f.deleted = true
		} else {
			f.removed = true
		}
		return ae.Null(), stats, nil
	}
	return ae.Value{}, stats, fmt.Errorf("unexpected draft mutation %+v", event)
}
func nativeDraftFixtureClient(f *nativeDraftDescriptorFixture) *Client {
	c := NewClient()
	c.shared.backend = BackendNative
	c.shared.nativeSender = f.send
	return c
}
func TestNativeDescriptorDraftSnapshotStrictRawSource(t *testing.T) {
	f := newNativeDraftDescriptorFixture()
	snapshot, e := nativeDraftFixtureClient(f).readDraftSnapshot("Personal", "15")
	if e != nil {
		t.Fatal(e)
	}
	if snapshot.Source != f.source.Text || snapshot.Message.Content != "World 世界\n" || snapshot.Message.ID != "15" || snapshot.Message.Mailbox != "Drafts" || snapshot.Message.Account != "Personal" {
		t.Fatalf("snapshot %+v", snapshot)
	}
	f.source = ae.Null()
	if _, e = nativeDraftFixtureClient(f).readDraftSnapshot("Personal", "15"); e == nil {
		t.Fatal("unavailable raw source accepted")
	}
}
func TestNativeDescriptorRichDraftRejectedBeforeOutgoingCreation(t *testing.T) {
	for _, operation := range []string{"preview", "send", "update"} {
		t.Run(operation, func(t *testing.T) {
			f := newNativeDraftDescriptorFixture()
			f.source = ae.String(strings.Replace(f.source.Text, "text/plain; charset=utf-8", "multipart/alternative; boundary=rich", 1))
			c := nativeDraftFixtureClient(f)
			var e error
			switch operation {
			case "preview":
				_, e = c.PreviewDraftSend("Personal", "15")
			case "send":
				_, e = c.SendDraftResult("Personal", "15")
			case "update":
				_, e = c.UpdateDraft("Personal", "15", DraftInput{Subject: "Updated", SubjectSet: true})
			}
			if e == nil || !strings.Contains(e.Error(), "multipart/alternative") {
				t.Fatalf("unexpected rich draft result: %v", e)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("unsupported draft was changed: %+v", event)
				}
			}
		})
	}
}
func TestNativeDescriptorDraftCleanupRetainsChangedOriginal(t *testing.T) {
	f := newNativeDraftDescriptorFixture()
	c := nativeDraftFixtureClient(f)
	snapshot, e := c.readDraftSnapshot("Personal", "15")
	if e != nil {
		t.Fatal(e)
	}
	f.changed = true
	if e = c.deleteUnchangedDraft(snapshot); e == nil || !strings.Contains(e.Error(), "changed") {
		t.Fatalf("changed original accepted: %v", e)
	}
	if f.removed {
		t.Fatal("changed original was deleted")
	}
}
func TestNativeDescriptorDraftCleanupVerifiesRemoval(t *testing.T) {
	f := newNativeDraftDescriptorFixture()
	c := nativeDraftFixtureClient(f)
	snapshot, e := c.readDraftSnapshot("Personal", "15")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.deleteUnchangedDraft(snapshot); e != nil {
		t.Fatal(e)
	}
	if !f.removed {
		t.Fatal("original retained")
	}
	_, e = c.readDraftSnapshot("Personal", "15")
	if !errors.Is(e, ErrNotFound) {
		t.Fatalf("removed draft returned %v", e)
	}
}

// This combines strict stored-draft descriptors with the outgoing transport.
// The complete SendDraftResult chain uses BackendNative and cannot reach Mail,
// a scripting engine, or an Envelope Index fixture.
type nativePlainDraftSendFixture struct {
	draft                                   *nativeDraftDescriptorFixture
	outgoing                                *outgoingDescriptorFixture
	events                                  []ae.Event
	sendErr                                 error
	changeAfterSend                         bool
	sourceReads, sendCalls, originalDeletes int
	sourceReadsAtSend, sourceReadsAtDelete  int
}

func newNativePlainDraftSendFixture() *nativePlainDraftSendFixture {
	outgoing := newOutgoingDescriptorFixture()
	outgoing.accountIDs = []string{"account-id"}
	return &nativePlainDraftSendFixture{draft: newNativeDraftDescriptorFixture(), outgoing: outgoing}
}
func (f *nativePlainDraftSendFixture) get(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, ref := range v.Items {
			value, e := f.get(ref)
			if e != nil {
				return ae.Value{}, e
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	class, prop, container := descriptorClass(v), descriptorProperty(v), v.Fields["from"]
	// Original and prepared copy share Drafts, with distinct IDs and MIME.
	if prop == "ID  " && descriptorClass(container) == "mssg" && container.Fields["seld"].Kind == "ordinal" && container.Fields["from"].Text == "draftbox" {
		ids := []ae.Value{}
		if !f.draft.removed {
			ids = append(ids, ae.Int(15))
		}
		if f.outgoing.saved {
			ids = append(ids, ae.Int(900))
		}
		return ae.List(ids...), nil
	}
	if descriptorClass(container) == "mssg" && container.Fields["seld"].Number == 900 {
		if !f.outgoing.saved {
			return ae.Value{}, &ae.Error{Code: -1728, Message: "prepared draft absent"}
		}
		if prop == "mbxp" {
			return ruleFixtureRef("draftbox"), nil
		}
		return f.outgoing.get(v)
	}
	if (prop == "radd" || prop == "pnam") && descriptorClass(container.Fields["from"]) == "mssg" && container.Fields["from"].Fields["seld"].Number == 900 {
		return f.outgoing.get(v)
	}

	if class == "mact" {
		return ae.List(ruleFixtureRef("account")), nil
	}
	if descriptorClass(container) == "mact" && container.Fields["seld"].Kind == "ordinal" && container.Fields["seld"].Code == "all " {
		switch prop {
		case "ID  ":
			return ae.List(ae.String("account-id")), nil
		case "pnam":
			return ae.List(ae.String("Personal")), nil
		case "isac":
			return ae.List(ae.Bool(true)), nil
		case "emad":
			return ae.List(stringsDescriptors([]string{"alias@example.com"})), nil
		}
	}
	if container.Text == "account" {
		switch prop {
		case "pALL":
			return ae.Record(map[string]ae.Value{"ID  ": ae.String("account-id"), "pnam": ae.String("Personal"), "emad": stringsDescriptors([]string{"alias@example.com"}), "isac": ae.Bool(true), "unme": ae.String("fixture-user"), "atyp": ae.Enum("etim")}), nil
		case "ID  ":
			return ae.String("account-id"), nil
		case "pnam":
			return ae.String("Personal"), nil
		}
	}
	if class == "attc" && descriptorClass(container) == "mssg" {
		return ae.List(), nil
	}
	if prop == "raso" && descriptorClass(container) == "mssg" {
		f.sourceReads++
	}
	if class == "mbxp" || container.Text == "draftbox" || prop == "drmb" || descriptorClass(container) == "mssg" || (prop == "radd" && descriptorClass(container.Fields["from"]) == "mssg") {
		return f.draft.get(v)
	}
	return f.outgoing.get(v)
}
func (f *nativePlainDraftSendFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, event)
	stats := ae.Stats{Events: 1, Attempted: true}
	switch event.Class + event.ID {
	case "coregetd":
		v, e := f.get(event.Params["----"])
		return v, stats, e
	case "emsgsend":
		f.sendCalls++
		f.sourceReadsAtSend = f.sourceReads
		if f.changeAfterSend {
			f.draft.changed = true
		}
		if f.sendErr != nil {
			return ae.Value{}, stats, f.sendErr
		}
		return ae.Bool(true), stats, nil
	case "coredelo":
		ref := event.Params["----"]
		if descriptorClass(ref) == "mssg" && ref.Fields["seld"].Number == 900 && ref.Fields["from"].Text == "draftbox" {
			if !f.outgoing.saved {
				return ae.Value{}, stats, fmt.Errorf("prepared copy already absent")
			}
			f.outgoing.draftDeletes++
			f.outgoing.saved = false
			return ae.Null(), stats, nil
		}

		if descriptorClass(ref) != "mssg" || ref.Fields["seld"].Number != 15 || ref.Fields["from"].Text != "draftbox" {
			return ae.Value{}, stats, fmt.Errorf("cleanup targeted a different message")
		}
		f.originalDeletes++
		f.sourceReadsAtDelete = f.sourceReads
		f.draft.removed = true
		return ae.Null(), stats, nil
	default:
		return f.outgoing.send(ctx, event)
	}
}
func (f *nativePlainDraftSendFixture) client() *Client {
	c := NewClient()
	c.shared.backend = BackendNative
	c.shared.nativeSender = f.send
	return c
}
func TestNativeDescriptorPlainDraftSendFunctionalChain(t *testing.T) {
	f := newNativePlainDraftSendFixture()
	c := f.client()
	result, e := c.SendDraftResult("Personal", "15")
	if e != nil {
		t.Fatal(e)
	}
	if c.Backend() != BackendNative || result.DraftID != "15" || result.Account != "Personal" || result.AccountID != "account-id" || result.Sender != "alias@example.com" || result.Status != "submitted" || !result.Submitted || !result.Sent || result.Uncertain || result.CleanupPending {
		t.Fatalf("native draft-send receipt %+v", result)
	}
	if f.sendCalls != 1 || f.originalDeletes != 1 || !f.draft.removed || f.outgoing.closeCalls != 1 {
		t.Fatalf("dispatch/cleanup counts: sends=%d originalDeletes=%d removed=%t outgoingCloses=%d", f.sendCalls, f.originalDeletes, f.draft.removed, f.outgoing.closeCalls)
	}
	if f.sourceReadsAtSend != 2 || f.sourceReadsAtDelete != 3 {
		t.Fatalf("missing original snapshot guards: beforeSend=%d beforeDelete=%d", f.sourceReadsAtSend, f.sourceReadsAtDelete)
	}
	if f.outgoing.fields["subj"].Text != "Hello" || f.outgoing.fields["ctnt"].Text != "World 世界\n" || f.outgoing.fields["sndr"].Text != "alias@example.com" || !outgoingEqual(f.outgoing.recipients["trcp"], []string{"to@example.com"}) || len(f.outgoing.recipients["crcp"])+len(f.outgoing.recipients["brcp"]) != 0 {
		t.Fatalf("draft fields changed: %+v recipients=%+v", f.outgoing.fields, f.outgoing.recipients)
	}
	sendIndex, deleteIndex := -1, -1
	readsAfterDelete := 0
	for i, event := range f.events {
		if event.Class+event.ID == "emsgsend" {
			sendIndex = i
		}
		if event.Class+event.ID == "coredelo" {
			deleteIndex = i
		}
		if deleteIndex >= 0 && event.ID == "getd" && descriptorProperty(event.Params["----"]) == "ID  " && descriptorClass(event.Params["----"].Fields["from"]) == "mssg" {
			readsAfterDelete++
		}
	}
	if sendIndex < 0 || deleteIndex <= sendIndex || readsAfterDelete == 0 {
		t.Fatalf("cleanup ordering/removal verification missing: send=%d delete=%d removalReads=%d", sendIndex, deleteIndex, readsAfterDelete)
	}
	for _, event := range f.events {
		if event.ID == "clos" && (!reflect.DeepEqual(event.Params["----"], f.outgoing.outgoing) || event.Params["savo"].Code != "no  ") {
			t.Fatalf("cleanup escaped owned outgoing composition: %+v", event)
		}
	}

	if f.outgoing.draftDeletes != 1 {
		t.Fatalf("prepared draft deletes=%d want1", f.outgoing.draftDeletes)
	}

}
func TestNativeDescriptorPlainDraftLostSendReplyNeverReplaysOrDeletesOriginal(t *testing.T) {
	f := newNativePlainDraftSendFixture()
	f.sendErr = &ae.Error{Code: -1712, Message: "send reply lost", Attempted: true}
	result, e := f.client().SendDraftResult("Personal", "15")
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Operation != "draft_send" || mutation.Phase != "submit" || !mutation.Attempted || !mutation.Uncertain || mutation.Submitted {
		t.Fatalf("lost reply mutation: result=%+v error=%v", result, e)
	}
	if result == nil || result.Status != "submission_uncertain" || !result.Uncertain || result.Submitted || result.Sent || result.CleanupPending {
		t.Fatalf("lost reply receipt %+v", result)
	}
	if f.sendCalls != 1 || f.originalDeletes != 0 || f.draft.removed || f.outgoing.closeCalls != 0 || f.sourceReadsAtSend != 2 {
		t.Fatalf("lost reply replayed or discarded data: sends=%d deletes=%d removed=%t closes=%d beforeSendReads=%d", f.sendCalls, f.originalDeletes, f.draft.removed, f.outgoing.closeCalls, f.sourceReadsAtSend)
	}
	if f.outgoing.draftDeletes != 0 {
		t.Fatalf("prepared draft deletes=%d want0", f.outgoing.draftDeletes)
	}

}
func TestNativeDescriptorPlainDraftChangedAfterSendRetainsOriginalWithoutResending(t *testing.T) {
	f := newNativePlainDraftSendFixture()
	f.changeAfterSend = true
	result, e := f.client().SendDraftResult("Personal", "15")
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Phase != "cleanup_original" || !mutation.Submitted || !mutation.Attempted || mutation.Uncertain || result == nil || !result.Submitted || !result.Sent || !result.CleanupPending || result.Status != "submitted_cleanup_pending" {
		t.Fatalf("changed original receipt=%+v error=%v", result, e)
	}
	if f.sendCalls != 1 || f.originalDeletes != 0 || f.draft.removed || !f.draft.changed || f.outgoing.closeCalls != 1 || f.sourceReads != 3 {
		t.Fatalf("changed original lost or resent: sends=%d deletes=%d removed=%t changed=%t closes=%d sourceReads=%d", f.sendCalls, f.originalDeletes, f.draft.removed, f.draft.changed, f.outgoing.closeCalls, f.sourceReads)
	}
	for _, event := range f.events {
		if event.ID == "clos" && (!reflect.DeepEqual(event.Params["----"], f.outgoing.outgoing) || event.Params["savo"].Code != "no  ") {
			t.Fatalf("cleanup escaped owned outgoing composition: %+v", event)
		}
	}

	if f.outgoing.draftDeletes != 1 {
		t.Fatalf("prepared draft deletes=%d want1", f.outgoing.draftDeletes)
	}

}

func TestNativeDescriptorDeletedDraftIsNotActive(t *testing.T) {
	for _, operation := range []string{"show", "preview", "send", "update", "delete", "attachments"} {
		t.Run(operation, func(t *testing.T) {
			f := newNativeDraftDescriptorFixture()
			f.deleted = true // ID, container, and source still resolve normally.
			c := nativeDraftFixtureClient(f)
			var err error
			switch operation {
			case "show":
				_, err = c.GetDraft("Personal", "15")
			case "preview":
				_, err = c.PreviewDraftSend("Personal", "15")
			case "send":
				_, err = c.SendDraftResult("Personal", "15")
			case "update":
				_, err = c.UpdateDraft("Personal", "15", DraftInput{Subject: "Updated", SubjectSet: true})
			case "delete":
				err = c.DeleteDraft("Personal", "15")
			case "attachments":
				_, err = c.nativeDraftAttachments(&Message{Account: "Personal", Mailbox: "Drafts", ID: "15"})
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleted draft operation error=%v, want not found", err)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("tombstone caused mutation: %+v", event)
				}
			}
		})
	}
}

func TestNativeDescriptorDraftDeletedStatusFailsClosed(t *testing.T) {
	for _, status := range []ae.Value{ae.Null(), ae.String("false"), ae.Int(0)} {
		f := newNativeDraftDescriptorFixture()
		f.deleteStatus = status
		c := nativeDraftFixtureClient(f)
		if _, err := c.GetDraft("Personal", "15"); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid status %+v: error=%v", status, err)
		}
		if err := c.deleteNativeDraft("Personal", "Drafts", "15"); err == nil {
			t.Fatalf("invalid status %+v permitted delete", status)
		}
		if f.removed {
			t.Fatal("invalid deleted status caused mutation")
		}
	}
	f := newNativeDraftDescriptorFixture()
	c := nativeDraftFixtureClient(f)
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.Class+event.ID == "coregetd" && descriptorProperty(event.Params["----"]) == "isdl" {
			return ae.Value{}, ae.Stats{}, &ae.Error{Code: -1728, Message: "status unavailable"}
		}
		return f.send(ctx, event)
	}
	if _, err := c.GetDraft("Personal", "15"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("failed deleted-status getter was accepted as absence: %v", err)
	}
}

func TestNativeDescriptorDraftCleanupAcceptsDeletedTombstone(t *testing.T) {
	f := newNativeDraftDescriptorFixture()
	f.deleteMarksDeleted = true
	c := nativeDraftFixtureClient(f)
	snapshot, err := c.readDraftSnapshot("Personal", "15")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.deleteUnchangedDraft(snapshot); err != nil {
		t.Fatal(err)
	}
	if !f.deleted || f.removed {
		t.Fatal("fixture did not retain addressable deleted tombstone")
	}
	deletes := 0
	for _, event := range f.events {
		if event.Class+event.ID == "coredelo" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("delete count=%d", deletes)
	}
}

func TestNativeDescriptorOriginalTombstoneBeforeCleanupNeedsNoDelete(t *testing.T) {
	f := newNativeDraftDescriptorFixture()
	c := nativeDraftFixtureClient(f)
	snapshot, err := c.readDraftSnapshot("Personal", "15")
	if err != nil {
		t.Fatal(err)
	}
	f.deleted = true
	if err := c.deleteUnchangedDraft(snapshot); err != nil {
		t.Fatal(err)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("already deleted original caused mutation: %+v", event)
		}
	}
}
