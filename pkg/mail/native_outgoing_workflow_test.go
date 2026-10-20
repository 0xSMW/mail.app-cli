package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestNativeSubmittedOwnedCleanup(t *testing.T) {
	for _, name := range []string{"closed", "acknowledged close remains readable", "close removes stored copy", "already absent", "changed body", "changed recipient", "changed attachment count", "same-count attachment replacement", "close failed", "cancelled after acknowledgement", "false acknowledgement"} {
		t.Run(name, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c = c.WithContext(ctx)
			c.shared.nativeSender = func(eventCtx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.Class+event.ID == "coreclos" && (name == "acknowledged close remains readable" || name == "close removes stored copy") {
					v, stats, err := f.send(eventCtx, event)
					if err == nil {
						if name == "acknowledged close remains readable" {
							f.closed = false
						} else {
							f.saved = false
						}
					}
					return v, stats, err
				}
				if event.Class+event.ID == "emsgsend" {
					if name == "false acknowledgement" {
						f.sendReply = ae.Bool(false)
					}
					v, stats, err := f.send(eventCtx, event)
					switch name {
					case "already absent":
						f.closed = true
					case "changed body":
						f.fields["ctnt"] = ae.String("external edit")
					case "changed recipient":
						f.recipients["trcp"] = []string{"external@example.com"}
					case "changed attachment count":
						f.files = append(f.files, "/tmp/external-file")
					case "same-count attachment replacement":
						f.files[0] = "/tmp/replacement.txt"
					case "close failed":
						f.closeErr = errors.New("close refused")
					case "cancelled after acknowledgement":
						cancel()
					}
					return v, stats, err
				}
				if f.sendCalls > 0 && name == "cancelled after acknowledgement" && eventCtx.Err() != nil {
					t.Fatal("cleanup inherited cancelled send context")
				}
				return f.send(eventCtx, event)
			}
			input := fixtureOutgoingInput()
			if name == "same-count attachment replacement" {
				path := filepath.Join(t.TempDir(), "original.txt")
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				input.Attachments = []string{path}
			}
			result, err := c.SendOutgoing(input)
			pending := name == "changed body" || name == "changed recipient" || name == "changed attachment count" || name == "same-count attachment replacement" || name == "close failed"
			if name == "false acknowledgement" {
				if err == nil || result.Submitted || f.closeCalls != 0 || f.draftDeletes != 0 {
					t.Fatalf("false ack result=%+v err=%v closes=%d", result, err, f.closeCalls)
				}
				return
			}
			if !result.Submitted || f.sendCalls != 1 {
				t.Fatalf("submission lost or replayed: %+v sends=%d", result, f.sendCalls)
			}
			if pending {
				var mutation *MutationError
				if !errors.As(err, &mutation) || mutation.Uncertain || !mutation.Submitted || mutation.Cleanup == "" || result.Status != "submitted_cleanup_pending" {
					t.Fatalf("pending result=%+v err=%v", result, err)
				}
			} else if err != nil || result.Status != "submitted" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			wantClose := 1
			if name == "already absent" || name == "changed body" || name == "changed recipient" || name == "changed attachment count" || name == "same-count attachment replacement" {
				wantClose = 0
			}
			if f.closeCalls != wantClose {
				t.Fatalf("closes=%d want=%d", f.closeCalls, wantClose)
			}
			wantDeletes := 1
			if pending || name == "close removes stored copy" {
				wantDeletes = 0
			}
			if f.draftDeletes != wantDeletes {
				t.Fatalf("stored deletes=%d want=%d", f.draftDeletes, wantDeletes)
			}
			if name == "acknowledged close remains readable" {
				if _, err := f.get(ae.Property("subj", f.outgoing)); err != nil {
					t.Fatalf("fixture descriptor did not remain readable: %v", err)
				}
			}
			for _, event := range f.events {
				if event.ID == "clos" && (!reflect.DeepEqual(event.Params["----"], f.outgoing) || event.Params["savo"].Code != "no  ") {
					t.Fatalf("unowned close: %+v", event)
				}
				if event.ID == "delo" {
					target := event.Params["----"]
					if descriptorClass(target) != "mssg" || target.Fields["seld"].Number != 900 || target.Fields["from"].Text != "outgoing-fixture-drafts" {
						t.Fatal("cleanup deleted an unowned stored message")
					}
				}
			}
		})
	}
}
