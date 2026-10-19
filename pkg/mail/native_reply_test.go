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

type threadedTransport struct {
	t                                    *testing.T
	events                               []ae.Event
	commandError                         error
	account, mailbox, original, outgoing ae.Value
	stableMailbox                        ae.Value
}

func newThreadedTransport(t *testing.T) *threadedTransport {
	account := ae.ByID("mact", ae.String("work-id"), ae.Null())
	mailbox := ae.Index("mbxp", 1, account)
	stableMailbox := ae.Value{Kind: "opaque", Data: "threaded-stable-mailbox"}
	return &threadedTransport{t: t, account: account, mailbox: mailbox, stableMailbox: stableMailbox,
		original: ae.ByID("mssg", ae.Int(42), stableMailbox), outgoing: ae.ByID("bcke", ae.Int(99), ae.Null())}
}

func (f *threadedTransport) read(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, item := range v.Items {
			value, err := f.read(item)
			if err != nil {
				return ae.Value{}, err
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	if reflect.DeepEqual(v, ae.All("mact", ae.Null())) {
		return ae.List(f.account), nil
	}
	selector := ae.ByTest("mact", ae.Comparison("=   ", ae.Property("pnam", ae.Examined()), ae.String("Work")), ae.Null())
	if reflect.DeepEqual(v, selector) {
		return ae.List(f.account), nil
	}
	if reflect.DeepEqual(v, f.mailbox) {
		return f.stableMailbox, nil
	}
	level := ae.All("mbxp", f.account)
	if reflect.DeepEqual(v, ae.Property("pnam", level)) {
		return ae.List(ae.String("INBOX")), nil
	}
	if reflect.DeepEqual(v, ae.Property("pnam", ae.All("mbxp", level))) {
		return ae.List(ae.List()), nil
	}
	if v.Kind == "object" && v.Fields["want"].Code == "prop" {
		container, code := v.Fields["from"], v.Fields["seld"].Code
		switch {
		case reflect.DeepEqual(container, ae.All("mact", ae.Null())):
			switch code {
			case "ID  ":
				return ae.List(ae.String("work-id")), nil
			case "pnam":
				return ae.List(ae.String("Work")), nil
			case "isac":
				return ae.List(ae.Bool(true)), nil
			case "emad":
				return ae.List(ae.List(ae.String("me@example.test"))), nil
			}
		case reflect.DeepEqual(container, f.account):
			switch code {
			case "pALL":
				return ae.Record(map[string]ae.Value{"pnam": ae.String("Work"), "ID  ": ae.String("work-id"), "isac": ae.Bool(true), "emad": ae.List(ae.String("me@example.test"))}), nil
			case "pnam":
				return ae.String("Work"), nil
			case "ID  ":
				return ae.String("work-id"), nil
			case "isac":
				return ae.Bool(true), nil
			case "emad":
				return ae.List(ae.String("me@example.test")), nil
			}
		case reflect.DeepEqual(container, f.mailbox) || reflect.DeepEqual(container, f.stableMailbox):
			switch code {
			case "pnam":
				return ae.String("INBOX"), nil
			case "mact":
				return f.account, nil
			case "mbxc":
				return ae.Null(), nil
			}
		case reflect.DeepEqual(container, f.original) && code == "ID  ":
			return ae.Int(42), nil
		}
	}
	return ae.Value{}, fmt.Errorf("unexpected read: %#v", v)
}

func (f *threadedTransport) send(_ context.Context, e ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, e)
	if e.Class == "core" && e.ID == "getd" {
		value, err := f.read(e.Params["----"])
		return value, ae.Stats{}, err
	}
	if e.Class == "emal" && (e.ID == "rpms" || e.ID == "fwms") {
		if !reflect.DeepEqual(e.Params["----"], f.original) {
			f.t.Fatalf("command did not target the exact original: %#v", e)
		}
		return f.outgoing, ae.Stats{}, f.commandError
	}
	return ae.Value{}, ae.Stats{}, fmt.Errorf("unexpected event: %#v", e)
}

func threadedTestInput(mode ReplyMode) ThreadedOutgoingInput {
	return ThreadedOutgoingInput{
		OutgoingInput:   OutgoingInput{Account: "Work", Subject: "Edited subject", Body: "Edited body", To: []string{"edited@example.test"}},
		OriginalAccount: "Work", OriginalMailbox: "INBOX", OriginalID: "42", Mode: mode,
	}
}

func TestNativeThreadedCommandUsesExactOriginalAndMode(t *testing.T) {
	for _, mode := range []ReplyMode{ReplyToSender, ReplyToAll, ForwardMessage} {
		t.Run(string(mode), func(t *testing.T) {
			f := newThreadedTransport(t)
			n := &nativeMail{ctx: context.Background(), sender: f.send}
			got, err := n.threadedOutgoing(threadedTestInput(mode))
			if err != nil || !reflect.DeepEqual(got, f.outgoing) {
				t.Fatalf("result = %#v, %v", got, err)
			}
			event := f.events[len(f.events)-1]
			wantID := "rpms"
			if mode == ForwardMessage {
				wantID = "fwms"
			}
			if event.Class != "emal" || event.ID != wantID || event.Params["ropw"].Kind != "bool" || event.Params["ropw"].Boolean {
				t.Fatalf("command = %#v", event)
			}
			all, hasAll := event.Params["rpal"]
			if mode == ForwardMessage {
				if hasAll {
					t.Fatal("forward included reply-to-all parameter")
				}
			} else if !hasAll || all.Kind != "bool" || all.Boolean != (mode == ReplyToAll) {
				t.Fatalf("reply-to-all parameter = %#v", all)
			}
		})
	}
}

func TestNativeThreadedDispatchFailureNeverCreatesUnthreadedMessage(t *testing.T) {
	for _, mode := range []ReplyMode{ReplyToSender, ReplyToAll, ForwardMessage} {
		t.Run(string(mode), func(t *testing.T) {
			f := newThreadedTransport(t)
			f.commandError = &ae.Error{Code: -1708, Message: "unsupported reply"}
			client := NewClient()
			if err := client.SetBackend(BackendNative); err != nil {
				t.Fatal(err)
			}
			client.shared.nativeSender = f.send
			_, err := client.SendThreadedOutgoing(threadedTestInput(mode))
			var mutation *MutationError
			if !errors.As(err, &mutation) || !errors.Is(err, f.commandError) || mutation.Attempted {
				t.Fatalf("dispatch error = %#v: %v", err, err)
			}
			dispatched := 0
			for _, event := range f.events {
				if event.Class == "emal" {
					dispatched++
				}
				if event.ID == "crel" || event.ID == "send" {
					t.Fatalf("native failure fell through: %#v", event)
				}
			}
			if dispatched != 1 {
				t.Fatalf("reply/forward dispatches = %d", dispatched)
			}
		})
	}
}

func TestNativeThreadedFailurePhaseSeparatesLookupFromCreation(t *testing.T) {
	for _, phase := range []string{"threaded_lookup", "threaded_create"} {
		t.Run(phase, func(t *testing.T) {
			f := newThreadedTransport(t)
			input := threadedTestInput(ReplyToSender)
			if phase == "threaded_lookup" {
				input.OriginalMailbox = "Missing"
			} else {
				f.commandError = &ae.Error{Code: -1712, Message: "lost creation receipt", Attempted: true}
			}
			n := &nativeMail{ctx: context.Background(), sender: f.send}
			if _, err := n.threadedOutgoing(input); err == nil {
				t.Fatal("expected failure")
			}
			if n.phaseName != phase {
				t.Fatalf("pending phase = %q, want %q", n.phaseName, phase)
			}
		})
	}
}

func TestThreadedOutgoingRejectsInvalidOriginalBeforeAutomation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ThreadedOutgoingInput)
		want   string
	}{
		{"mode", func(in *ThreadedOutgoingInput) { in.Mode = "unknown" }, "invalid reply mode"},
		{"mailbox", func(in *ThreadedOutgoingInput) { in.OriginalMailbox = "" }, "original account and mailbox"},
		{"message", func(in *ThreadedOutgoingInput) { in.OriginalID = "bad" }, "invalid original message ID"},
		{"account", func(in *ThreadedOutgoingInput) { in.Account = "Other" }, "reply account must match"},
		{"recipient", func(in *ThreadedOutgoingInput) { in.To = nil }, "at least one recipient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := threadedTestInput(ReplyToSender)
			tt.change(&input)
			client := NewClient()
			if err := client.SetBackend(BackendNative); err != nil {
				t.Fatal(err)
			}
			client.shared.nativeSender = func(context.Context, ae.Event) (ae.Value, ae.Stats, error) {
				t.Fatal("invalid input dispatched automation")
				return ae.Value{}, ae.Stats{}, nil
			}
			if _, err := client.SendThreadedOutgoing(input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNativeThreadedOutgoingPreservesEditedFieldsAndRecipients(t *testing.T) {
	for _, mode := range []ReplyMode{ReplyToSender, ReplyToAll, ForwardMessage} {
		t.Run(string(mode), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			account := ae.Index("mact", 1, ae.Null())
			box := ae.Index("mbxp", 1, account)
			stableBox := ae.Value{Kind: "opaque", Data: "edited-fields-stable-mailbox"}
			original := ae.ByID("mssg", ae.Int(42), stableBox)
			client := fixtureOutgoingClient(f)
			commands, removed := 0, 0
			client.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				ref := event.Params["----"]
				if event.Class == "emal" {
					commands++
					f.events = append(f.events, event)
					if !reflect.DeepEqual(ref, original) {
						t.Fatalf("wrong original: %#v", ref)
					}
					// Mail's own generated values must all yield to the editor.
					f.fields["subj"], f.fields["ctnt"], f.fields["sndr"] = ae.String("Generated subject"), ae.String("Generated quotation"), ae.String("primary@example.com")
					for _, class := range []string{"trcp", "crcp", "brcp"} {
						f.recipients[class] = []string{"inherited@example.com"}
					}
					return f.outgoing, ae.Stats{}, nil
				}
				if event.Class == "core" && event.ID == "getd" {
					if reflect.DeepEqual(ref, box) {
						return stableBox, ae.Stats{}, nil
					}
					level := ae.All("mbxp", account)
					if reflect.DeepEqual(ref, ae.Property("pnam", level)) {
						return ae.List(ae.String("INBOX")), ae.Stats{}, nil
					}
					if reflect.DeepEqual(ref, ae.Property("pnam", ae.All("mbxp", level))) {
						return ae.List(ae.List()), ae.Stats{}, nil
					}
					if reflect.DeepEqual(ref, ae.Property("pnam", box)) || reflect.DeepEqual(ref, ae.Property("pnam", stableBox)) {
						return ae.String("INBOX"), ae.Stats{}, nil
					}
					if reflect.DeepEqual(ref, ae.Property("mact", box)) || reflect.DeepEqual(ref, ae.Property("mact", stableBox)) {
						return account, ae.Stats{}, nil
					}
					if reflect.DeepEqual(ref, ae.Property("mbxc", box)) || reflect.DeepEqual(ref, ae.Property("mbxc", stableBox)) {
						return ae.Null(), ae.Stats{}, nil
					}
					if reflect.DeepEqual(ref, ae.Property("ID  ", original)) {
						return ae.Int(42), ae.Stats{}, nil
					}
					if class := descriptorClass(ref); class == "trcp" || class == "crcp" || class == "brcp" {
						items := make([]ae.Value, len(f.recipients[class]))
						for i := range items {
							items[i] = ae.Index(class, i+1, f.outgoing)
						}
						return ae.List(items...), ae.Stats{}, nil
					}
				}
				if event.Class == "core" && event.ID == "cnte" {
					if !reflect.DeepEqual(ref, ae.All("atts", ae.Property("ctnt", f.outgoing))) {
						t.Fatalf("unexpected attachment count target: %#v", ref)
					}
					return ae.Int(int64(len(f.files))), ae.Stats{}, nil
				}
				if event.Class == "core" && event.ID == "delo" {
					if !reflect.DeepEqual(ref.Fields["from"], f.outgoing) {
						t.Fatalf("recipient deletion escaped owned outgoing: %#v", ref)
					}
					f.recipients[descriptorClass(ref)] = nil
					removed++
					return ae.Null(), ae.Stats{}, nil
				}
				return f.send(ctx, event)
			}
			input := ThreadedOutgoingInput{OutgoingInput: fixtureOutgoingInput(), OriginalAccount: "Personal", OriginalMailbox: "INBOX", OriginalID: "42", Mode: mode}
			result, err := client.SendThreadedOutgoing(input)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Submitted || f.sendCalls != 1 || commands != 1 || removed != 3 {
				t.Fatalf("result=%+v sends=%d commands=%d inherited recipients removed=%d", result, f.sendCalls, commands, removed)
			}
			if f.fields["subj"].Text != input.Subject || f.fields["ctnt"].Text != input.Body || f.fields["sndr"].Text != "alias@example.com" {
				t.Fatalf("generated values survived editor replacement: %+v", f.fields)
			}
			if !reflect.DeepEqual(f.recipients["trcp"], input.To) || !reflect.DeepEqual(f.recipients["crcp"], input.Cc) || !reflect.DeepEqual(f.recipients["brcp"], input.Bcc) {
				t.Fatalf("edited recipients were lost: %+v", f.recipients)
			}
			for _, event := range f.events {
				if event.ID == "crel" && event.Params["kocl"].Code == "bcke" {
					t.Fatal("threaded compose created an ordinary outgoing")
				}
			}
		})
	}
}
