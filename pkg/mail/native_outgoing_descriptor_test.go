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
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// The fixture interprets typed descriptors only. It cannot invoke Mail, a
// scripting engine, or a subprocess, even when a test requests submission.
type outgoingDescriptorFixture struct {
	accountNames               []string
	accountAddresses           [][]string
	accountIDs                 []string
	accountEnabled             []bool
	fields                     map[string]ae.Value
	recipients                 map[string][]string
	files                      []string
	events                     []ae.Event
	outgoing                   ae.Value
	makeErr, sendErr, closeErr error
	sendReply                  ae.Value
	corrupt                    string
	sendCalls, closeCalls      int
	saved                      bool
	closed                     bool
}

func newOutgoingDescriptorFixture() *outgoingDescriptorFixture {
	return &outgoingDescriptorFixture{accountNames: []string{"Personal"}, accountAddresses: [][]string{{"primary@example.com", "alias@example.com"}}, fields: map[string]ae.Value{}, recipients: map[string][]string{}, outgoing: ae.ByID("bcke", ae.Int(91), ae.Null()), sendReply: ae.Bool(true)}
}
func descriptorClass(v ae.Value) string { return v.Fields["want"].Code }
func descriptorProperty(v ae.Value) string {
	if descriptorClass(v) == "prop" {
		return v.Fields["seld"].Code
	}
	return ""
}
func descriptorAccountIndex(v ae.Value) int {
	if descriptorClass(v) == "mact" {
		return int(v.Fields["seld"].Number) - 1
	}
	return -1
}
func stringsDescriptors(values []string) ae.Value {
	list := make([]ae.Value, len(values))
	for i, s := range values {
		list[i] = ae.String(s)
	}
	return ae.List(list...)
}
func (f *outgoingDescriptorFixture) accountID(i int) string {
	if i < len(f.accountIDs) {
		return f.accountIDs[i]
	}
	return fmt.Sprintf("account-%d", i)
}
func (f *outgoingDescriptorFixture) accountIsEnabled(i int) bool {
	if i < len(f.accountEnabled) {
		return f.accountEnabled[i]
	}
	return true
}
func (f *outgoingDescriptorFixture) accountIndex(v ae.Value) int {
	if descriptorClass(v) == "mact" && v.Fields["form"].Code == "ID  " && v.Fields["seld"].Kind == "string" {
		for i := range f.accountNames {
			if f.accountID(i) == v.Fields["seld"].Text {
				return i
			}
		}
		return -1
	}
	return descriptorAccountIndex(v)
}
func (f *outgoingDescriptorFixture) get(v ae.Value) (ae.Value, error) {
	if f.closed && reflect.DeepEqual(v.Fields["from"], f.outgoing) {
		return ae.Value{}, &ae.Error{Code: -1728, Message: "owned composition absent"}
	}
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, ref := range v.Items {
			item, e := f.get(ref)
			if e != nil {
				return ae.Value{}, e
			}
			items[i] = item
		}
		return ae.List(items...), nil
	}
	class := descriptorClass(v)
	property := descriptorProperty(v)
	container := v.Fields["from"]
	if class == "mact" {
		items := make([]ae.Value, len(f.accountNames))
		for i := range items {
			items[i] = ae.Index("mact", i+1, ae.Null())
		}
		return ae.List(items...), nil
	}
	if class == "mbxp" {
		return ae.List(ruleFixtureRef("outgoing-fixture-drafts")), nil
	}
	if property == "drmb" {
		return ruleFixtureRef("outgoing-fixture-draft-root"), nil
	}
	if container.Text == "outgoing-fixture-drafts" {
		if property == "pnam" {
			return ae.String("Drafts"), nil
		}
		if property == "mact" {
			return ae.ByID("mact", ae.String(f.accountID(0)), ae.Null()), nil
		}
	}
	if descriptorClass(container) == "mssg" {
		if property == "ID  " {
			if container.Fields["form"].Code == "ID  " {
				return container.Fields["seld"], nil
			}
			if f.saved {
				return ae.List(ae.Int(15)), nil
			}
			return ae.List(), nil
		}
		if property == "mbxp" {
			return ruleFixtureRef("outgoing-fixture-drafts"), nil
		}
		if property == "raso" {
			names := make([]string, len(f.files))
			payloads := make([][]byte, len(f.files))
			for i, path := range f.files {
				names[i] = filepath.Base(path)
				var err error
				payloads[i], err = os.ReadFile(path)
				if err != nil {
					return ae.Value{}, err
				}
			}
			return ae.String(attachmentProofMIME(names, payloads)), nil
		}
	}
	if descriptorClass(container) == "attc" && property == "pnam" {
		names := make([]string, len(f.files))
		for i, path := range f.files {
			names[i] = filepath.Base(path)
		}
		return stringsDescriptors(names), nil
	}
	if property != "" {
		if descriptorClass(container) == "mact" && container.Fields["seld"].Kind == "ordinal" && container.Fields["seld"].Code == "all " {
			values := make([]ae.Value, len(f.accountNames))
			for i := range values {
				switch property {
				case "ID  ":
					values[i] = ae.String(f.accountID(i))
				case "pnam":
					values[i] = ae.String(f.accountNames[i])
				case "isac":
					values[i] = ae.Bool(f.accountIsEnabled(i))
				case "emad":
					values[i] = stringsDescriptors(f.accountAddresses[i])
				default:
					return ae.Value{}, fmt.Errorf("unexpected account column %s", property)
				}
			}
			return ae.List(values...), nil
		}
		if i := f.accountIndex(container); i >= 0 {
			if property == "pALL" {
				return ae.Record(map[string]ae.Value{"pnam": ae.String(f.accountNames[i]), "ID  ": ae.String(f.accountID(i)), "isac": ae.Bool(true), "emad": stringsDescriptors(f.accountAddresses[i]), "unme": ae.String("fixture-user"), "atyp": ae.Enum("etim")}), nil
			}
			switch property {
			case "pnam":
				return ae.String(f.accountNames[i]), nil
			case "ID  ":
				return ae.String(f.accountID(i)), nil
			case "isac":
				return ae.Bool(f.accountIsEnabled(i)), nil
			case "emad":
				return stringsDescriptors(f.accountAddresses[i]), nil
			}
		}
		if property == "radd" {
			return stringsDescriptors(f.recipients[descriptorClass(container)]), nil
		}
		if property == "atfn" {
			list := make([]ae.Value, len(f.files))
			for i, p := range f.files {
				list[i] = ae.File(p)
			}
			return ae.List(list...), nil
		}
		if property == f.corrupt {
			return ae.String("altered"), nil
		}
		if value, ok := f.fields[property]; ok {
			return value, nil
		}
	}
	return ae.Value{}, fmt.Errorf("unexpected read descriptor %+v", v)
}
func (f *outgoingDescriptorFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, event)
	stats := ae.Stats{Events: 1, Attempted: true}
	switch event.Class + event.ID {
	case "coregetd":
		v, e := f.get(event.Params["----"])
		return v, stats, e
	case "coresetd":
		f.fields[descriptorProperty(event.Params["----"])] = event.Params["data"]
		return ae.Null(), stats, nil
	case "corecrel":
		class := event.Params["kocl"].Code
		properties := event.Params["prdt"].Fields
		switch class {
		case "bcke":
			if f.makeErr != nil {
				return ae.Value{}, stats, f.makeErr
			}
			for k, v := range properties {
				f.fields[k] = v
			}
			return f.outgoing, stats, nil
		case "trcp", "crcp", "brcp":
			f.recipients[class] = append(f.recipients[class], properties["radd"].Text)
			return ae.Index(class, len(f.recipients[class]), f.outgoing), stats, nil
		case "atts":
			f.files = append(f.files, properties["atfn"].Text)
			return ae.Index("atts", len(f.files), ae.Property("ctnt", f.outgoing)), stats, nil
		}
	case "corecnte":
		ref := event.Params["----"]
		if descriptorClass(ref) != "atts" || descriptorProperty(ref.Fields["from"]) != "ctnt" {
			return ae.Value{}, stats, fmt.Errorf("unexpected attachment count target")
		}
		return ae.Int(int64(len(f.files))), stats, nil
	case "coresave":
		if !reflect.DeepEqual(event.Params["----"], f.outgoing) {
			return ae.Value{}, stats, fmt.Errorf("saved an unowned composition")
		}
		f.saved = true
		return ae.Null(), stats, nil
	case "emsgsend":
		f.sendCalls++
		return f.sendReply, stats, f.sendErr
	case "coreclos":
		f.closeCalls++
		if f.closeErr == nil {
			f.closed = true
		}
		return ae.Null(), stats, f.closeErr
	}
	return ae.Value{}, stats, fmt.Errorf("unexpected event %s/%s", event.Class, event.ID)
}
func fixtureOutgoingClient(f *outgoingDescriptorFixture) *Client {
	c := NewClient()
	c.shared.nativeSender = f.send
	c.shared.backend = BackendNative
	return c
}
func fixtureOutgoingInput() OutgoingInput {
	return OutgoingInput{Account: "Personal", Sender: "Alias <alias@example.com>", Subject: "Réponse 世界", Body: "Hello\n 世界 \n", To: []string{"one@example.com", "two@example.com"}, Cc: []string{"copy@example.com"}, Bcc: []string{"hidden@example.com"}}
}
func TestNativeDescriptorOutgoingFidelityAndFiles(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	c := fixtureOutgoingClient(f)
	input := fixtureOutgoingInput()
	path := filepath.Join(t.TempDir(), `résumé "quoted",世界.txt`)
	if e := os.WriteFile(path, []byte("attachment bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	input.Attachments = []string{path}
	result, e := c.SendOutgoing(input)
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "submitted" || !result.Submitted || result.Sender != "alias@example.com" || f.sendCalls != 1 {
		t.Fatalf("result=%+v sends=%d", result, f.sendCalls)
	}
	if f.fields["subj"].Text != input.Subject || f.fields["ctnt"].Text != input.Body || !reflect.DeepEqual(f.files, input.Attachments) {
		t.Fatalf("fields/files changed: %+v %+v", f.fields, f.files)
	}
	if !reflect.DeepEqual(f.recipients["trcp"], input.To) || !reflect.DeepEqual(f.recipients["crcp"], input.Cc) || !reflect.DeepEqual(f.recipients["brcp"], input.Bcc) {
		t.Fatalf("recipients changed: %+v", f.recipients)
	}
	for _, event := range f.events {
		if event.ID == "crel" && event.Params["kocl"].Code == "atts" {
			insertion := event.Params["insh"]
			if insertion.Kind != "insertion" || insertion.Fields["kpos"].Code != "afte" {
				t.Fatalf("attachment is not a native after-paragraph insertion: %+v", insertion)
			}
			paragraph := insertion.Fields["kobj"]
			if descriptorClass(paragraph) != "cpar" || paragraph.Fields["form"].Code != "indx" || paragraph.Fields["seld"].Kind != "ordinal" || paragraph.Fields["seld"].Code != "last" {
				t.Fatalf("attachment paragraph requires absolute last ordinal: %+v", paragraph)
			}

			if event.Params["prdt"].Fields["atfn"].Kind != "file" {
				t.Fatal("attachment did not use a native file descriptor")
			}
		}
	}
}
func TestNativeDescriptorOutgoingAliasAmbiguityPreventsCreation(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.accountNames = append(f.accountNames, "Work")
	f.accountAddresses = append(f.accountAddresses, []string{"ALIAS@example.com"})
	result, e := fixtureOutgoingClient(f).SendOutgoing(fixtureOutgoingInput())
	if e == nil {
		t.Fatal("shared sender accepted")
	}
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Attempted || mutation.Uncertain {
		t.Fatalf("unexpected error: %v", e)
	}
	for _, event := range f.events {
		if event.ID == "crel" {
			t.Fatal("created outgoing before proving alias ownership")
		}
	}
	if result.Submitted {
		t.Fatal("reported submission")
	}
}
func TestNativeDescriptorOutgoingSubmissionUncertaintyNeverRetries(t *testing.T) {
	for _, cause := range []error{&ae.Error{Code: -1712, Message: "reply timeout", Attempted: true}, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			f.sendErr = cause
			result, e := fixtureOutgoingClient(f).SendOutgoing(fixtureOutgoingInput())
			var mutation *MutationError
			if !errors.As(e, &mutation) || !mutation.Attempted || !mutation.Uncertain || mutation.Submitted || result.Submitted {
				t.Fatalf("result=%+v error=%v", result, e)
			}
			if f.sendCalls != 1 || f.closeCalls != 0 {
				t.Fatalf("send replay or postdispatch cleanup: sends=%d close=%d", f.sendCalls, f.closeCalls)
			}
		})
	}
}
func TestNativeDescriptorOutgoingReadbackFailureCleansOnlyOwnedObject(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.corrupt = "subj"
	f.closeErr = errors.New("close refused")
	_, e := fixtureOutgoingClient(f).SendOutgoing(fixtureOutgoingInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Attempted || mutation.Uncertain || mutation.Cleanup == "" {
		t.Fatalf("unexpected error %v", e)
	}
	if f.sendCalls != 0 || f.closeCalls != 1 {
		t.Fatalf("sends=%d closes=%d", f.sendCalls, f.closeCalls)
	}
	for _, event := range f.events {
		if event.ID == "clos" {
			if !reflect.DeepEqual(event.Params["----"], f.outgoing) || event.Params["savo"].Code != "no  " {
				t.Fatalf("cleanup not scoped to owned compose: %+v", event)
			}
		}
	}
}
func TestNativeDescriptorOutgoingPreviewNeverComposes(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	result, e := fixtureOutgoingClient(f).PreviewOutgoing(fixtureOutgoingInput())
	if e != nil || result.Status != "preview" || result.Submitted {
		t.Fatalf("result=%+v error=%v", result, e)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("preview mutation %+v", event)
		}
	}
}

func TestNativeDescriptorOutgoingSaveVerifiesStoredFingerprint(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous_%t", ambiguous), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			box := ruleFixtureRef("draftbox")
			account := ae.Index("mact", 1, ae.Null())
			stored := false
			var get func(ae.Value) (ae.Value, error)
			get = func(v ae.Value) (ae.Value, error) {
				if v.Kind == "list" {
					items := make([]ae.Value, len(v.Items))
					for i, ref := range v.Items {
						value, e := get(ref)
						if e != nil {
							return ae.Value{}, e
						}
						items[i] = value
					}
					return ae.List(items...), nil
				}
				class, prop, container := descriptorClass(v), descriptorProperty(v), v.Fields["from"]
				if class == "mbxp" {
					return ae.List(box), nil
				}
				if class == "mssg" {
					if !stored {
						return ae.List(), nil
					}
					refs := []ae.Value{ae.ByID("mssg", ae.Int(15), box)}
					if ambiguous {
						refs = append(refs, ae.ByID("mssg", ae.Int(16), box))
					}
					return ae.List(refs...), nil
				}
				if prop == "drmb" {
					return ruleFixtureRef("draftroot"), nil
				}
				if container.Text == box.Text {
					switch prop {
					case "pnam":
						return ae.String("Drafts"), nil
					case "mact":
						return account, nil
					}
				}
				if prop == "ID  " && descriptorClass(container) == "mssg" {
					if container.Fields["form"].Code == "ID  " {
						return container.Fields["seld"], nil
					}
					if !stored {
						return ae.List(), nil
					}
					if ambiguous {
						return ae.List(ae.Int(15), ae.Int(16)), nil
					}
					return ae.List(ae.Int(15)), nil
				}
				if descriptorClass(container) == "mssg" && prop == "mbxp" {
					return box, nil
				}
				if prop == "pnam" && descriptorClass(container) == "attc" {
					return ae.List(), nil
				}
				return f.get(v)
			}
			c := NewClient()
			c.shared.backend = BackendNative
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.Class+event.ID == "coregetd" {
					f.events = append(f.events, event)
					v, e := get(event.Params["----"])
					return v, ae.Stats{Events: 1}, e
				}
				if event.Class+event.ID == "coresave" {
					f.events = append(f.events, event)
					stored = true
					return ae.Null(), ae.Stats{Events: 1, Attempted: true}, nil
				}
				return f.send(ctx, event)
			}
			input := fixtureOutgoingInput()
			result, e := c.composeOutgoing(input, "save")
			if ambiguous {
				var mutation *MutationError
				if !errors.As(e, &mutation) || !mutation.Attempted || !mutation.Saved || !mutation.Uncertain || mutation.Phase != "save_verification" {
					t.Fatalf("ambiguous saved result=%+v error=%v", result, e)
				}
				if f.closeCalls != 1 {
					t.Fatalf("saved ambiguity unexpectedly cleaned compose: %d closes", f.closeCalls)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if result.Status != "saved" || result.Submitted || result.Draft == nil || result.Draft.ID != "15" || result.Draft.Mailbox != "Drafts" || result.Draft.Account != "Personal" {
				t.Fatalf("saved receipt %+v", result)
			}
			if f.fields["ctnt"].Text != input.Body || f.sendCalls != 0 {
				t.Fatalf("save content or send changed %+v sends %d", f.fields, f.sendCalls)
			}
		})
	}
}

func TestNativeDescriptorOutgoingAttachmentCancellationUsesBoundedOwnedCleanup(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := fixtureOutgoingClient(f).WithContext(ctx)
	path := filepath.Join(t.TempDir(), "pending.txt")
	if e := os.WriteFile(path, []byte("pending"), 0600); e != nil {
		t.Fatal(e)
	}
	input := fixtureOutgoingInput()
	input.Attachments = []string{path}
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if e := ctx.Err(); e != nil {
			return ae.Value{}, ae.Stats{}, e
		}
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
			f.events = append(f.events, event)
			cancel()
			return ae.List(), ae.Stats{Events: 1}, nil
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(input)
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Attempted || mutation.Uncertain || mutation.Cleanup != "" || mutation.Phase != "attachments" {
		t.Fatalf("cancellation result %v", e)
	}
	if f.sendCalls != 0 || f.closeCalls != 1 {
		t.Fatalf("cancelled readiness sent or failed owned cleanup: send=%d close=%d", f.sendCalls, f.closeCalls)
	}
}
func TestNativeDescriptorOutgoingLostCreationReceiptReportsCleanupUncertainty(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.makeErr = &ae.Error{Code: -1712, Message: "creation reply lost", Attempted: true}
	_, e := fixtureOutgoingClient(f).SendOutgoing(fixtureOutgoingInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Attempted || mutation.Uncertain || mutation.Cleanup == "" {
		t.Fatalf("lost creation identity was hidden: %v", e)
	}
	if f.sendCalls != 0 || f.closeCalls != 0 {
		t.Fatalf("used unidentified object: send=%d close=%d", f.sendCalls, f.closeCalls)
	}
}

func TestNativeDescriptorOutgoingAttachmentReadinessPollsTypedMissingFiles(t *testing.T) {
	for _, pending := range []ae.Value{ae.Null(), ae.TypeCode("msng"), ae.Enum("msng"), {Kind: "opaque", Code: "msng"}, ae.File(""), ae.String("")} {
		t.Run(pending.Kind+pending.Code, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			path := filepath.Join(t.TempDir(), "eventually-ready.txt")
			if e := os.WriteFile(path, []byte("ready"), 0600); e != nil {
				t.Fatal(e)
			}
			input := fixtureOutgoingInput()
			input.Attachments = []string{path}
			reads := 0
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
					reads++
					if reads == 1 {
						return ae.List(pending), ae.Stats{Events: 1}, nil
					}
				}
				return f.send(ctx, event)
			}
			result, e := c.SendOutgoing(input)
			if e != nil {
				t.Fatal(e)
			}
			expectedReads := 6
			if pending.Code == "msng" {
				expectedReads = 5
			}
			if reads != expectedReads || f.sendCalls != 1 || !result.Submitted || f.closeCalls != 1 {
				t.Fatalf("readiness result=%+v reads=%d sends=%d closes=%d", result, reads, f.sendCalls, f.closeCalls)
			}
		})
	}
}
func TestNativeDescriptorOutgoingAttachmentMalformedDiagnosticExcludesPrivateValues(t *testing.T) {
	for _, value := range []ae.Value{{Kind: "opaque", Code: "obj ", Text: "/private/account/inbox/file.txt", Data: "private-descriptor-data"}, ae.String("file://invalid%zz/private/account")} {
		_, e := outgoingAttachmentName(value)
		if e == nil || errors.Is(e, errOutgoingAttachmentPending) {
			t.Fatalf("malformed descriptor accepted: %v", e)
		}
		if !strings.Contains(e.Error(), "kind=") || !strings.Contains(e.Error(), "code=") {
			t.Fatalf("missing descriptor diagnostic: %v", e)
		}
		if strings.Contains(e.Error(), value.Text) || strings.Contains(e.Error(), "private-descriptor-data") || strings.Contains(e.Error(), "/private") {
			t.Fatalf("private descriptor exposed: %v", e)
		}
	}
}

func TestNativeDescriptorOutgoingAttachmentReadinessPollsMissingProperty(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	c := fixtureOutgoingClient(f)
	path := filepath.Join(t.TempDir(), "ready-name.txt")
	if e := os.WriteFile(path, []byte("ready"), 0600); e != nil {
		t.Fatal(e)
	}
	input := fixtureOutgoingInput()
	input.Attachments = []string{path}
	reads := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
			reads++
			if reads == 1 {
				return ae.TypeCode("msng"), ae.Stats{Events: 1}, nil
			}
			if reads == 2 {
				return ae.List(), ae.Stats{Events: 1}, nil
			}
		}
		return f.send(ctx, event)
	}
	result, e := c.SendOutgoing(input)
	if e != nil {
		t.Fatal(e)
	}
	if reads != 5 || !result.Submitted || f.sendCalls != 1 {
		t.Fatalf("readiness result=%+v reads=%d sends=%d", result, reads, f.sendCalls)
	}
}

func TestNativeDescriptorOutgoingAttachmentUnsupportedGetterNeedsExactNativeCount(t *testing.T) {
	for _, getterResult := range []struct {
		name  string
		value ae.Value
		err   error
	}{{name: "missingobject", err: &ae.Error{Code: -1728, Message: "atfn unavailable"}}, {name: "typedmissing", value: ae.List(ae.TypeCode("msng"))}} {
		t.Run(getterResult.name, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			path := filepath.Join(t.TempDir(), "owned-file.txt")
			if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
				t.Fatal(e)
			}
			input := fixtureOutgoingInput()
			input.Attachments = []string{path}
			counts := 0
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
					return getterResult.value, ae.Stats{Events: 1}, getterResult.err
				}
				if event.ID == "cnte" {
					counts++
					if counts <= 2 {
						return ae.Int(0), ae.Stats{Events: 1}, nil
					}
				}
				return f.send(ctx, event)
			}
			result, e := c.SendOutgoing(input)
			if e != nil {
				t.Fatal(e)
			}
			if counts != 7 || f.sendCalls != 1 || !result.Submitted || f.closeCalls != 1 {
				t.Fatalf("count guard result=%+v counts=%d sends=%d closes=%d", result, counts, f.sendCalls, f.closeCalls)
			}
		})
	}
}
func TestNativeDescriptorOutgoingAttachmentCountMismatchFailsBeforeDispatch(t *testing.T) {
	for _, count := range []ae.Value{ae.Int(2), ae.Int(-1), ae.String("private-count-data")} {
		t.Run(count.Kind+fmt.Sprint(count.Number), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			path := filepath.Join(t.TempDir(), "only-one.txt")
			if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
				t.Fatal(e)
			}
			input := fixtureOutgoingInput()
			input.Attachments = []string{path}
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
					return ae.List(ae.TypeCode("msng")), ae.Stats{Events: 1}, nil
				}
				if event.ID == "cnte" && len(f.files) > 0 {
					return count, ae.Stats{Events: 1}, nil
				}
				return f.send(ctx, event)
			}
			_, e := c.SendOutgoing(input)
			var mutation *MutationError
			if !errors.As(e, &mutation) || mutation.Phase != "attachments" || mutation.Attempted || mutation.Uncertain || f.sendCalls != 0 || f.closeCalls != 1 {
				t.Fatalf("count mismatch error=%v sends=%d closes=%d", e, f.sendCalls, f.closeCalls)
			}
			if strings.Contains(e.Error(), "private-count-data") {
				t.Fatal("count diagnostic exposed private descriptor")
			}
		})
	}
}
func TestNativeDescriptorOutgoingAttachmentGetterPermissionFailureNeverFallsBack(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	c := fixtureOutgoingClient(f)
	path := filepath.Join(t.TempDir(), "permission.txt")
	if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	input := fixtureOutgoingInput()
	input.Attachments = []string{path}
	counts := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
			return ae.Value{}, ae.Stats{Events: 1}, &ae.Error{Code: -1743, Message: "automation denied"}
		}
		if event.ID == "cnte" {
			counts++
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(input)
	var native *ae.Error
	if !errors.As(e, &native) || native.Code != -1743 || counts != 1 || f.sendCalls != 0 || f.closeCalls != 1 {
		t.Fatalf("permission failure weakened: error=%v counts=%d sends=%d closes=%d", e, counts, f.sendCalls, f.closeCalls)
	}
}
func TestNativeDescriptorOutgoingAttachmentMalformedInsertionReceiptFailsBeforeReadyCount(t *testing.T) {
	for _, receipt := range []ae.Value{ae.Int(1), ae.Bool(false)} {
		t.Run(receipt.Kind, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			path := filepath.Join(t.TempDir(), "unidentified.txt")
			if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
				t.Fatal(e)
			}
			input := fixtureOutgoingInput()
			input.Attachments = []string{path}
			counts := 0
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "crel" && event.Params["kocl"].Code == "atts" {
					_, stats, e := f.send(ctx, event)
					return receipt, stats, e
				}
				if event.ID == "cnte" {
					counts++
				}
				return f.send(ctx, event)
			}
			_, e := c.SendOutgoing(input)
			var mutation *MutationError
			if !errors.As(e, &mutation) || mutation.Phase != "attachments" || mutation.Attempted || !strings.Contains(e.Error(), "unsupported receipt") || counts != 1 || f.sendCalls != 0 || f.closeCalls != 1 {
				t.Fatalf("unidentified insert accepted: error=%v counts=%d sends=%d closes=%d", e, counts, f.sendCalls, f.closeCalls)
			}
		})
	}
}

func TestNativeDescriptorOutgoingAttachmentMissingMakeReceiptAcceptedOnlyWithReadyCount(t *testing.T) {
	for _, receipt := range []ae.Value{ae.Null(), ae.TypeCode("msng")} {
		t.Run(receipt.Kind, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			path := filepath.Join(t.TempDir(), "acknowledged.txt")
			if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
				t.Fatal(e)
			}
			input := fixtureOutgoingInput()
			input.Attachments = []string{path}
			counts := 0
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "crel" && event.Params["kocl"].Code == "atts" {
					_, stats, e := f.send(ctx, event)
					return receipt, stats, e
				}
				if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
					return ae.Value{}, ae.Stats{Events: 1}, &ae.Error{Code: -1728, Message: "atfn unsupported"}
				}
				if event.ID == "cnte" {
					counts++
				}
				return f.send(ctx, event)
			}
			result, e := c.SendOutgoing(input)
			if e != nil {
				t.Fatal(e)
			}
			if counts != 6 || f.sendCalls != 1 || !result.Submitted || f.closeCalls != 1 {
				t.Fatalf("missing receipt readiness result=%+v counts=%d sends=%d closes=%d", result, counts, f.sendCalls, f.closeCalls)
			}
		})
	}
}
func TestNativeDescriptorOutgoingAttachmentMissingMakeReceiptWithZeroCountNeverDispatches(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := fixtureOutgoingClient(f).WithContext(ctx)
	path := filepath.Join(t.TempDir(), "not-ready.txt")
	if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	input := fixtureOutgoingInput()
	input.Attachments = []string{path}
	counts := 0
	var cancelledAt time.Time
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "crel" && event.Params["kocl"].Code == "atts" {
			_, stats, e := f.send(ctx, event)
			return ae.TypeCode("msng"), stats, e
		}
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "atfn" {
			return ae.List(ae.TypeCode("msng")), ae.Stats{Events: 1}, nil
		}
		if event.ID == "cnte" {
			counts++
			if counts == 2 {
				cancelledAt = time.Now()
				cancel()
			}
			return ae.Int(0), ae.Stats{Events: 1}, nil
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(input)
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Phase != "attachments" || mutation.Attempted || mutation.Uncertain || counts < 2 || f.sendCalls != 0 || f.closeCalls != 1 {
		t.Fatalf("notready receipt accepted: error=%v counts=%d sends=%d closes=%d", e, counts, f.sendCalls, f.closeCalls)
	}
	if cancelledAt.IsZero() || time.Since(cancelledAt) > time.Second {
		t.Fatal("attachment readiness ignored bounded context")
	}
}
func TestNativeDescriptorOutgoingAttachmentMakeErrorNeverCountsAsAcknowledgement(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	c := fixtureOutgoingClient(f)
	path := filepath.Join(t.TempDir(), "failed-insert.txt")
	if e := os.WriteFile(path, []byte("bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	input := fixtureOutgoingInput()
	input.Attachments = []string{path}
	counts := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "crel" && event.Params["kocl"].Code == "atts" {
			return ae.TypeCode("msng"), ae.Stats{Events: 1}, &ae.Error{Code: -1700, Message: "insert coercion refused"}
		}
		if event.ID == "cnte" {
			counts++
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(input)
	var native *ae.Error
	if !errors.As(e, &native) || native.Code != -1700 || counts != 1 || f.sendCalls != 0 || f.closeCalls != 1 {
		t.Fatalf("failed make treated as acknowledgment: error=%v counts=%d sends=%d closes=%d", e, counts, f.sendCalls, f.closeCalls)
	}
}

func TestNativeDescriptorOutgoingBodyDifferenceReportsShapeWithoutMessageText(t *testing.T) {
	body := "Confidential 世界"
	for _, actual := range []string{body + "\n", "\n" + body + "\n", body + "\ufffc", body + " changed"} {
		e := outgoingBodyDifference(actual, body)
		if e == nil || strings.Contains(e.Error(), body) || strings.Contains(e.Error(), "Confidential") || strings.Contains(e.Error(), "世界") || strings.Contains(e.Error(), " changed") {
			t.Fatalf("body diagnostic exposed text: %v", e)
		}
		if !strings.Contains(e.Error(), "expected bytes=") || !strings.Contains(e.Error(), "readback bytes=") {
			t.Fatalf("missing body structural diagnostic: %v", e)
		}
	}
	if e := outgoingBodyDifference(body+"\n", body); !strings.Contains(e.Error(), "extra terminal LF=true") {
		t.Fatalf("terminal LF shape lost: %v", e)
	}
	if e := outgoingBodyDifference("\n"+body+"\n", body); !strings.Contains(e.Error(), "extra leading-and-terminal LF=true") {
		t.Fatalf("paragraph shape lost: %v", e)
	}
}

func TestNativeDescriptorOutgoingSendSuppliesExplicitFinalParagraphLF(t *testing.T) {
	for _, body := range []string{"One line 世界", "  padded 世界  ", "already terminated\n", "two terminal lines\n\n", ""} {
		t.Run(fmt.Sprintf("bytes_%d", len(body)), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			input := fixtureOutgoingInput()
			input.Body = body
			// Emulate Mail's observed space placeholder for an unterminated rich-text
			// paragraph. An explicit LF avoids it before the strict readback guard.
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				v, stats, e := f.send(ctx, event)
				if e == nil && event.ID == "crel" && event.Params["kocl"].Code == "bcke" && !strings.HasSuffix(f.fields["ctnt"].Text, "\n") {
					f.fields["ctnt"] = ae.String(f.fields["ctnt"].Text + " ")
				}
				return v, stats, e
			}
			result, e := c.SendOutgoing(input)
			if e != nil {
				t.Fatal(e)
			}
			expected := body
			if !strings.HasSuffix(expected, "\n") {
				expected += "\n"
			}
			if f.fields["ctnt"].Text != expected || !result.Submitted || f.sendCalls != 1 {
				t.Fatalf("canonical body changed whitespace: expected=%q actual=%q result=%+v sends=%d", expected, f.fields["ctnt"].Text, result, f.sendCalls)
			}
		})
	}
}
func TestNativeDescriptorOutgoingCanonicalParagraphStillGuardsUserWhitespace(t *testing.T) {
	for _, alter := range []func(string) string{func(s string) string { return strings.TrimSpace(s) + "\n" }, func(s string) string { return s + " " }, func(s string) string { return " " + s }} {
		f := newOutgoingDescriptorFixture()
		c := fixtureOutgoingClient(f)
		input := fixtureOutgoingInput()
		input.Body = "  padded 世界  "
		c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
			v, stats, e := f.send(ctx, event)
			if e == nil && event.ID == "setd" && descriptorProperty(event.Params["----"]) == "sndr" {
				f.fields["ctnt"] = ae.String(alter(f.fields["ctnt"].Text))
			}
			return v, stats, e
		}
		_, e := c.SendOutgoing(input)
		var mutation *MutationError
		if !errors.As(e, &mutation) || mutation.Phase != "recipient_verification" || mutation.Attempted || mutation.Uncertain || f.sendCalls != 0 || f.closeCalls != 1 {
			t.Fatalf("whitespace corruption accepted: error=%v sends=%d closes=%d", e, f.sendCalls, f.closeCalls)
		}
	}
}

func TestNativeDescriptorOutgoingSenderUsesFiveFreshColumnsAndStableIDReference(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.accountNames = append(f.accountNames, "Work")
	f.accountAddresses = append(f.accountAddresses, []string{"work@example.com"})
	n := &nativeMail{ctx: context.Background(), sender: f.send}
	ref, id, sender, e := n.outgoingSender(fixtureOutgoingInput())
	if e != nil {
		t.Fatal(e)
	}
	if id != "account-0" || sender != "alias@example.com" || descriptorClass(ref) != "mact" || ref.Fields["form"].Code != "ID  " || ref.Fields["seld"].Kind != "string" || ref.Fields["seld"].Text != id {
		t.Fatalf("sender/account reference drift: ref=%+v id=%s sender=%s", ref, id, sender)
	}
	expected := []string{"ID  ", "pnam", "isac", "emad", "ID  "}
	if len(f.events) != len(expected) {
		t.Fatalf("metadata required %d events, expected5", len(f.events))
	}
	for i, event := range f.events {
		object := event.Params["----"]
		all := object.Fields["from"]
		if event.Class+event.ID != "coregetd" || descriptorProperty(object) != expected[i] || descriptorClass(all) != "mact" || all.Fields["seld"].Kind != "ordinal" || all.Fields["seld"].Code != "all " {
			t.Fatalf("metadata column %d event %+v", i, event)
		}
	}
}
func TestNativeDescriptorOutgoingSenderDuplicateIDsNamesAndDisabledFailBeforeCreation(t *testing.T) {
	for _, scenario := range []string{"duplicate IDs", "duplicate names", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			switch scenario {
			case "duplicate IDs":
				f.accountNames = append(f.accountNames, "Work")
				f.accountAddresses = append(f.accountAddresses, []string{"work@example.com"})
				f.accountIDs = []string{"shared-id", "shared-id"}
			case "duplicate names":
				f.accountNames = append(f.accountNames, "Personal")
				f.accountAddresses = append(f.accountAddresses, []string{"other@example.com"})
			case "disabled":
				f.accountEnabled = []bool{false}
			}
			result, e := fixtureOutgoingClient(f).SendOutgoing(fixtureOutgoingInput())
			var mutation *MutationError
			if !errors.As(e, &mutation) || mutation.Phase != "account_resolution" || mutation.Attempted || mutation.Uncertain || result.Submitted || f.sendCalls != 0 || f.closeCalls != 0 {
				t.Fatalf("unsafe metadata accepted: result=%+v error=%v", result, e)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("metadata failure mutatedMail: %+v", event)
				}
			}
		})
	}
}
func TestNativeDescriptorOutgoingSenderReorderedColumnsFailBeforeCreation(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.accountNames = append(f.accountNames, "Work")
	f.accountAddresses = append(f.accountAddresses, []string{"work@example.com"})
	c := fixtureOutgoingClient(f)
	idReads := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		object := event.Params["----"]
		if event.ID == "getd" && descriptorProperty(object) == "ID  " && descriptorClass(object.Fields["from"]) == "mact" {
			idReads++
			if idReads == 2 {
				f.events = append(f.events, event)
				return ae.List(ae.String("account-1"), ae.String("account-0")), ae.Stats{Events: 1}, nil
			}
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(fixtureOutgoingInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || !strings.Contains(e.Error(), "collection changed") || mutation.Attempted || mutation.Uncertain || f.sendCalls != 0 || f.closeCalls != 0 || idReads != 2 {
		t.Fatalf("reordered account columns accepted: error=%v reads=%d", e, idReads)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("reorder mutatedMail: %+v", event)
		}
	}
}
func TestNativeDescriptorOutgoingSenderColumnPermissionFailureHasNoFallback(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	c := fixtureOutgoingClient(f)
	permission := &ae.Error{Code: -1743, Message: "account address projection denied"}
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "emad" {
			f.events = append(f.events, event)
			return ae.Value{}, ae.Stats{Events: 1}, permission
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(fixtureOutgoingInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || !errors.Is(e, permission) || mutation.Attempted || mutation.Uncertain || f.sendCalls != 0 || f.closeCalls != 0 {
		t.Fatalf("permission failure weakened: %v", e)
	}
	for _, event := range f.events {
		if event.ID != "getd" || descriptorProperty(event.Params["----"]) == "pALL" {
			t.Fatalf("permission triggered fallback: %+v", event)
		}
	}
}
func TestNativeDescriptorOutgoingSenderOwnershipIsRecheckedAfterComposition(t *testing.T) {
	f := newOutgoingDescriptorFixture()
	f.accountNames = append(f.accountNames, "Work")
	f.accountAddresses = append(f.accountAddresses, []string{"work@example.com"})
	c := fixtureOutgoingClient(f)
	aliasReads := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.ID == "getd" && descriptorProperty(event.Params["----"]) == "emad" {
			aliasReads++
			if aliasReads == 2 {
				f.accountAddresses[1] = append(f.accountAddresses[1], "ALIAS@example.com")
			}
		}
		return f.send(ctx, event)
	}
	_, e := c.SendOutgoing(fixtureOutgoingInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Phase != "recipient_verification" || mutation.Attempted || mutation.Uncertain || f.sendCalls != 0 || f.closeCalls != 1 || aliasReads != 2 {
		t.Fatalf("sender ownership was cached acrossguards: error=%v sends=%d closes=%d reads=%d", e, f.sendCalls, f.closeCalls, aliasReads)
	}
}
