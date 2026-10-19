package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func attachmentProofMIME(names []string, payloads [][]byte) string {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for i, name := range names {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/octet-stream")
		header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		header.Set("Content-Transfer-Encoding", "base64")
		part, err := writer.CreatePart(header)
		if err != nil {
			panic(err)
		}
		_, err = part.Write([]byte(base64.StdEncoding.EncodeToString(payloads[i])))
		if err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return "From: alias@example.com\r\nIn-Reply-To: <parent@example.com>\r\nReferences: <parent@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"" + writer.Boundary() + "\"\r\n\r\n" + body.String()
}

func TestNativeAttachmentPayloadReadiness(t *testing.T) {
	for _, scenario := range []string{"complete", "missing", "wrong bytes", "extra file", "corrupt encoding", "truncated", "placeholder", "ambiguous", "account mismatch", "cancelled", "input changed", "compose changed", "false acknowledgement", "lost acknowledgement", "plain complete", "plain absent after close", "plain backend absent", "plain source changed", "plain stored recipient changed", "plain account drift", "plain ID getter failed", "plain cancelled after acknowledgement", "plain false acknowledgement", "plain inherited attachment", "plain delete failed", "plain account drift after delete", "plain source getter failed", "plain owned snapshot vanished", "plain owned recipient getter failed", "plain stored snapshot vanished", "plain stored source vanished", "plain stored recipient getter failed", "plain vanished before delete", "plain stale IDs after root vanished", "plain stale IDs remain", "plain close missing root vanished", "plain close missing root present", "rendered body", "rendered body changed text", "rendered body missing payload", "rendered body corrupt payload"} {
		t.Run(scenario, func(t *testing.T) {
			mode := strings.TrimPrefix(scenario, "plain ")
			plain := strings.HasPrefix(scenario, "plain ")
			f := newOutgoingDescriptorFixture()
			if mode == "false acknowledgement" {
				f.sendReply = ae.Bool(false)
			}
			if mode == "lost acknowledgement" {
				f.sendErr = &ae.Error{Code: -1712, Attempted: true, Message: "lost send acknowledgement"}
			}
			input := fixtureOutgoingInput()
			input.Body = "Multiline café 日本語 🚀\nLiteral <angle> & symbols.\n"
			names := []string{"résumé 世界.txt", "second.bin"}
			payloads := [][]byte{[]byte("first café 日本語\n"), {0, 1, 2, 255, 4}}
			if plain {
				names = nil
				payloads = nil
			}
			for i, name := range names {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, payloads[i], 0600); err != nil {
					t.Fatal(err)
				}
				input.Attachments = append(input.Attachments, path)
			}
			source := attachmentProofMIME(names, payloads)
			if plain {
				source = "From: alias@example.com\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + input.Body
			}
			if mode == "inherited attachment" {
				source = attachmentProofMIME([]string{"unrequested.bin"}, [][]byte{[]byte("unrequested")})
			}
			switch mode {
			case "rendered body missing payload":
				source = "Content-Type: text/plain\r\n\r\nno payload"
			case "rendered body corrupt payload":
				source = strings.Replace(source, base64.StdEncoding.EncodeToString(payloads[0]), "%%%", 1)
			case "missing":
				source = "Content-Type: text/plain\r\n\r\nno payload"
			case "wrong bytes":
				source = attachmentProofMIME(names, [][]byte{[]byte("wrong"), payloads[1]})
			case "extra file":
				source = attachmentProofMIME(append(append([]string{}, names...), "extra"), append(append([][]byte{}, payloads...), []byte("extra")))
			case "corrupt encoding":
				source = strings.Replace(source, base64.StdEncoding.EncodeToString(payloads[0]), "%%%", 1)
			case "truncated":
				source = source[:len(source)-12]
			case "placeholder":
				source = strings.Replace(source, "Content-Transfer-Encoding: base64", "X-Apple-Content-Length: 999999\r\nContent-Transfer-Encoding: base64", 1)
			}
			box := ruleFixtureRef("attachment-proof-drafts")
			account := ae.ByID("mact", ae.String("account-0"), ae.Null())
			saved := false
			removed := false
			deletes := 0
			storedRecipientChanged := false
			rootGone := false
			staleReads := 0
			vanished := false
			saveCalls := 0
			sourceReads := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var get func(ae.Value) (ae.Value, error)
			get = func(v ae.Value) (ae.Value, error) {
				if v.Kind == "list" {
					values := make([]ae.Value, len(v.Items))
					for i, item := range v.Items {
						value, err := get(item)
						if err != nil {
							return ae.Value{}, err
						}
						values[i] = value
					}
					return ae.List(values...), nil
				}
				class, prop, container := descriptorClass(v), descriptorProperty(v), v.Fields["from"]
				if f.sendCalls > 0 && reflect.DeepEqual(container, f.outgoing) && prop == "ctnt" && mode == "owned snapshot vanished" {
					f.closed = true
					removed = true
					return ae.Value{}, &ae.Error{Code: -1728, Message: "owned root vanished"}
				}
				if f.sendCalls > 0 && prop == "radd" && reflect.DeepEqual(container.Fields["from"], f.outgoing) && mode == "owned recipient getter failed" {
					return ae.Value{}, &ae.Error{Code: -1728, Message: "owned recipient unavailable"}
				}
				if f.sendCalls > 0 && prop == "radd" && descriptorClass(container.Fields["from"]) == "mssg" && mode == "stored recipient getter failed" {
					return ae.Value{}, &ae.Error{Code: -1728, Message: "stored recipient unavailable"}
				}

				if class == "mbxp" {
					return ae.List(box), nil
				}
				if prop == "drmb" {
					return ruleFixtureRef("draft-root"), nil
				}
				if container.Text == box.Text {
					switch prop {
					case "pnam":
						return ae.String("Drafts"), nil
					case "mact":
						return account, nil
					}
				}
				if prop == "radd" && descriptorClass(container.Fields["from"]) == "mssg" && storedRecipientChanged && descriptorClass(container) == "trcp" {
					return stringsDescriptors([]string{"edited@example.com"}), nil
				}
				if descriptorClass(container) == "mssg" {
					if container.Fields["form"].Code == "ID  " && container.Fields["seld"].Number == 99 {
						t.Fatal("inspected an unowned stored draft")
					}
					if f.sendCalls > 0 && container.Fields["form"].Code == "ID  " {
						if rootGone {
							return ae.Value{}, &ae.Error{Code: -1728, Message: "stored root vanished"}
						}
						if !vanished && prop == "sndr" && (mode == "stored snapshot vanished" || mode == "stale IDs after root vanished" || mode == "stale IDs remain") {
							vanished = true
							rootGone = true
							if mode == "stored snapshot vanished" {
								removed = true
							} else if mode == "stale IDs remain" {
								staleReads = -1
							} else {
								staleReads = 2
							}
							return ae.Value{}, &ae.Error{Code: -1728, Message: "stored root vanished during snapshot"}
						}
					}
					if prop == "ID  " {
						if container.Fields["form"].Code == "ID  " {
							return container.Fields["seld"], nil
						}
						if rootGone {
							if staleReads == 0 {
								removed = true
							} else if staleReads > 0 {
								staleReads--
							}
						}
						if mode == "ID getter failed" && f.sendCalls > 0 {
							return ae.Value{}, &ae.Error{Code: -1712, Message: "ID enumeration failed"}
						}
						if !saved || removed {
							return ae.List(ae.Int(99)), nil
						}
						if mode == "ambiguous" {
							return ae.List(ae.Int(99), ae.Int(15), ae.Int(16)), nil
						}
						return ae.List(ae.Int(99), ae.Int(15)), nil
					}
					if prop == "ctnt" && strings.HasPrefix(mode, "rendered body") {
						return ae.String("\n\u2028" + input.Body), nil
					}
					if prop == "mbxp" {
						if mode == "account mismatch" {
							return ruleFixtureRef("foreign-box"), nil
						}
						return box, nil
					}
					if prop == "raso" {
						if f.sendCalls > 0 && mode == "stored source vanished" {
							rootGone = true
							removed = true
							return ae.Value{}, &ae.Error{Code: -1728, Message: "stored source vanished"}
						}
						if mode == "source getter failed" && f.sendCalls > 0 {
							return ae.Value{}, &ae.Error{Code: -1728, Message: "stored source unavailable"}
						}
						sourceReads++
						return ae.String(source), nil
					}
				}
				if container.Text == "foreign-box" {
					if prop == "pnam" {
						return ae.String("Drafts"), nil
					}
					if prop == "mact" {
						return ruleFixtureRef("foreign-account"), nil
					}
				}
				if container.Text == "foreign-account" && prop == "pnam" {
					return ae.String("Other"), nil
				}
				if descriptorClass(container) == "attc" && prop == "pnam" {
					return stringsDescriptors(names), nil
				}
				return f.get(v)
			}
			c := fixtureOutgoingClient(f).WithContext(ctx)
			c.shared.nativeSender = func(eventCtx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.Class+event.ID == "coregetd" {
					f.events = append(f.events, event)
					v, err := get(event.Params["----"])
					return v, ae.Stats{Events: 1}, err
				}
				if event.Class+event.ID == "coresave" {
					if !reflect.DeepEqual(event.Params["----"], f.outgoing) {
						t.Fatal("saved a different composition")
					}
					f.events = append(f.events, event)
					saveCalls++
					saved = true
					if mode == "cancelled" {
						cancel()
					}
					if mode == "input changed" {
						if err := os.WriteFile(input.Attachments[0], []byte("changed"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "compose changed" {
						f.fields["ctnt"] = ae.String("changed")
					}
					return ae.Null(), ae.Stats{Events: 1, Attempted: true}, nil
				}
				if event.Class+event.ID == "coreclos" && f.sendCalls > 0 && (mode == "close missing root vanished" || mode == "close missing root present") {
					f.events = append(f.events, event)
					f.closeCalls++
					if mode == "close missing root vanished" {
						f.closed = true
						removed = true
					}
					return ae.Value{}, ae.Stats{Events: 1, Attempted: true}, &ae.Error{Code: -1728, Message: "close could not resolve object"}
				}
				if event.Class+event.ID == "coreclos" && mode == "complete" && f.sendCalls == 0 {
					t.Fatal("closed composition before send")
				}
				if event.Class+event.ID == "emsgsend" && sourceReads == 0 {
					t.Fatal("sent before raw MIME payload proof")
				}
				if event.Class+event.ID == "coredelo" {
					f.events = append(f.events, event)
					target := event.Params["----"]
					if descriptorClass(target) != "mssg" || target.Fields["seld"].Number != 15 || !reflect.DeepEqual(target.Fields["from"], box) {
						t.Fatal("deleted an unowned stored draft")
					}
					deletes++
					if mode == "vanished before delete" {
						removed = true
						rootGone = true
						return ae.Value{}, ae.Stats{Events: 1, Attempted: true}, &ae.Error{Code: -1728, Message: "stored draft vanished before delete"}
					}
					if mode == "delete failed" {
						return ae.Value{}, ae.Stats{Events: 1, Attempted: true}, errors.New("delete failed")
					}
					removed = true
					if mode == "account drift after delete" {
						f.accountIDs = []string{"replacement-account"}
					}
					return ae.Null(), ae.Stats{Events: 1, Attempted: true}, nil
				}
				v, stats, err := f.send(eventCtx, event)
				if event.Class+event.ID == "corecrel" && event.Params["kocl"].Code == "atts" && len(f.files) == len(input.Attachments) && strings.HasPrefix(mode, "rendered body") {
					body := input.Body
					if mode == "rendered body changed text" {
						body += "changed"
					}
					f.fields["ctnt"] = ae.String(body + strings.Repeat("\ufffc", len(input.Attachments)))
				}
				if event.Class+event.ID == "emsgsend" && f.sendReply.Boolean && err == nil {
					switch mode {
					case "source changed":
						source = "Message-ID: <changed@example.com>\r\n" + source
					case "stored recipient changed":
						storedRecipientChanged = true
					case "account drift":
						f.accountIDs = []string{"replacement-account"}
					case "backend absent":
						f.closed = true
					case "cancelled after acknowledgement":
						cancel()
					}
				}
				if event.Class+event.ID == "coreclos" && mode == "absent after close" {
					removed = true
				}
				return v, stats, err
			}
			result, err := c.SendOutgoing(input)
			success := mode == "complete" || mode == "absent after close" || mode == "backend absent" || mode == "cancelled after acknowledgement" || mode == "owned snapshot vanished" || mode == "stored snapshot vanished" || mode == "stored source vanished" || mode == "vanished before delete" || mode == "stale IDs after root vanished" || mode == "close missing root vanished" || mode == "rendered body"
			pending := mode == "source changed" || mode == "stored recipient changed" || mode == "account drift" || mode == "ID getter failed" || mode == "delete failed" || mode == "account drift after delete" || mode == "source getter failed" || mode == "owned recipient getter failed" || mode == "stored recipient getter failed" || mode == "stale IDs remain" || mode == "close missing root present"
			if success {
				wantCloses := 1
				if mode == "backend absent" || mode == "owned snapshot vanished" {
					wantCloses = 0
				}
				if err != nil || !result.Submitted || f.sendCalls != 1 || saveCalls != 1 || f.closeCalls != wantCloses || !removed {
					t.Fatalf("result=%+v err=%v sends=%d saves=%d closes=%d", result, err, f.sendCalls, saveCalls, f.closeCalls)
				}
			} else if pending {
				var mutation *MutationError
				if !errors.As(err, &mutation) || !mutation.Submitted || mutation.Uncertain || result.Status != "submitted_cleanup_pending" || !result.Submitted || f.sendCalls != 1 {
					t.Fatalf("unsafe cleanup pending result=%+v err=%v sends=%d", result, err, f.sendCalls)
				}
				if mode != "delete failed" && mode != "account drift after delete" && deletes != 0 {
					t.Fatal("deleted changed or inaccessible draft")
				}
			} else if mode == "false acknowledgement" || mode == "lost acknowledgement" {
				var mutation *MutationError
				if !errors.As(err, &mutation) || !mutation.Attempted || !mutation.Uncertain || mutation.Submitted || !mutation.Saved || result.Submitted || f.sendCalls != 1 || f.closeCalls != 0 || deletes != 0 {
					t.Fatalf("uncertain attached send result=%+v err=%v sends=%d closes=%d", result, err, f.sendCalls, f.closeCalls)
				}
			} else {
				var mutation *MutationError
				if !errors.As(err, &mutation) || mutation.Attempted || mutation.Uncertain || !mutation.Saved || err == nil || result.Submitted || f.sendCalls != 0 {
					t.Fatalf("unsafe preflight scenario=%s result=%+v err=%v sends=%d", scenario, result, err, f.sendCalls)
				}
			}
			if deletes > 1 {
				t.Fatal("repeated stored draft deletion")
			}
			if mode == "owned recipient getter failed" && f.closeCalls != 0 {
				t.Fatal("closed owned root after nested getter failure")
			}
			for _, event := range f.events {
				if event.ID == "clos" && !reflect.DeepEqual(event.Params["----"], f.outgoing) {
					t.Fatal("closed unowned object")
				}
			}
		})
	}
}

type attachmentProofRepeatReader byte

func (r attachmentProofRepeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestNativeAttachmentPayloadProofAboveCacheLimit(t *testing.T) {
	const size = maxMessageFileSize + 1
	var source strings.Builder
	header := "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=large.bin\r\nContent-Transfer-Encoding: binary\r\n\r\n"
	source.Grow(len(header) + size)
	source.WriteString(header)
	if _, err := io.CopyN(&source, attachmentProofRepeatReader('x'), size); err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, attachmentProofRepeatReader('x'), size); err != nil {
		t.Fatal(err)
	}
	expected := outgoingAttachmentProof{name: "large.bin"}
	copy(expected.hash[:], hash.Sum(nil))
	raw := source.String()
	if err := proveOutgoingMIMEAttachments(context.Background(), raw, []outgoingAttachmentProof{expected}); err != nil {
		t.Fatalf("native send inherited cache cap: %v", err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	err = walkLocalAttachments(context.Background(), parsed.Header.Get, parsed.Body, 0, func(_ Attachment, reader io.Reader) (int64, error) { return io.Copy(io.Discard, reader) })
	if err == nil || err.Error() != "decoded attachment exceeds message limit" {
		t.Fatalf("cached wrapper cap changed: %v", err)
	}
}

func TestNativeAttachmentPayloadProofPlainTextLFBytes(t *testing.T) {
	payload := "first café 日本語\nsecond line\n"
	source := "Content-Type: text/plain; charset=utf-8\r\nContent-Disposition: attachment; filename=second.txt\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + payload
	expected := []outgoingAttachmentProof{{name: "second.txt", hash: sha256.Sum256([]byte(payload))}}
	if err := proveOutgoingMIMEAttachments(context.Background(), source, expected); err != nil {
		t.Fatalf("LF text attachment bytes changed: %v", err)
	}
	if err := proveOutgoingMIMEAttachments(context.Background(), strings.ReplaceAll(source, "second line\n", "second line\r\n"), expected); err == nil {
		t.Fatal("accepted changed text attachment line endings")
	}
}

func TestNativePreparedAttachmentRenderedBodyFingerprint(t *testing.T) {
	body := "\n  first café 日本語\nsecond line  \n"
	markers := strings.Repeat("\ufffc", 2)
	type specimen struct {
		action, body, owned, stored string
		count                       int
		want                        bool
	}
	cases := map[string]specimen{
		"observed":                   {"send", body, body + markers, "\n\u2028" + body, 2, true},
		"wrong count":                {"send", body, body + markers, "\n\u2028" + body, 1, false},
		"missing marker":             {"send", body, body + "\ufffc", "\n\u2028" + body, 2, false},
		"wrong location":             {"send", body, markers + body, "\n\u2028" + body, 2, false},
		"changed owned text":         {"send", body, body + "changed" + markers, "\n\u2028" + body, 2, false},
		"changed owned whitespace":   {"send", body, strings.TrimLeft(body, "\n ") + markers, "\n\u2028" + body, 2, false},
		"changed stored leading LF":  {"send", body, body + markers, "\n\u2028" + strings.TrimPrefix(body, "\n"), 2, false},
		"changed stored trailing LF": {"send", body, body + markers, "\n\u2028" + strings.TrimSuffix(body, "\n"), 2, false},
		"extra stored whitespace":    {"send", body, body + markers, "\n\u2028" + body + " ", 2, false},
		"different separator":        {"send", body, body + markers, "\n\u2029" + body, 2, false},
		"extra separator":            {"send", body, body + markers, "\n\u2028\u2028" + body, 2, false},
		"separator inside text":      {"send", body, body + markers, "\n\u2028" + body + "\u2028", 2, false},
		"plain send":                 {"send", body, body, "\n\u2028" + body, 0, false},
		"save captured mapping":      {"save", body, body + markers, "\n\u2028" + body, 2, true},
		"save without capture":       {"save", body, "", "\n\u2028" + body, 2, false},
		"literal user object":        {"send", body + "\ufffc", body + "\ufffc" + markers, "\n\u2028" + body + "\ufffc", 2, false},
		"normal exact remains":       {"save", body, "", body, 0, true},
		"normal leading LF remains":  {"save", body, "", "\n" + body, 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run := nativeOutgoingRun{action: tc.action, body: tc.body, preSaveOwnedBody: tc.owned, input: OutgoingInput{Attachments: make([]string, tc.count)}}
			if got := run.savedDraftBodyMatches(tc.stored); got != tc.want {
				t.Fatalf("match=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestNativeSaveCapturesAndGuardsOwnedAttachmentBody(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			input := fixtureOutgoingInput()
			path := filepath.Join(t.TempDir(), "file.bin")
			if err := os.WriteFile(path, []byte{0, 1, 255}, 0600); err != nil {
				t.Fatal(err)
			}
			input.Attachments = []string{path}
			f.files = []string{path}
			f.fields["ctnt"] = ae.String(input.Body + "\ufffc")
			f.fields["subj"] = ae.String(input.Subject)
			f.fields["sndr"] = ae.String("alias@example.com")
			f.recipients = map[string][]string{"trcp": input.To, "crcp": input.Cc, "brcp": input.Bcc}
			n := &nativeMail{ctx: context.Background(), sender: func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				value, stats, err := f.send(ctx, event)
				if changed && event.Class+event.ID == "coresave" {
					f.fields["ctnt"] = ae.String("changed after save")
				}
				return value, stats, err
			}}
			run := nativeOutgoingRun{n: n, input: input, action: "save", owned: f.outgoing, body: input.Body, result: &OutgoingResult{}}
			err := run.dispatch()
			if run.preSaveOwnedBody != input.Body+"\ufffc" || !run.saved || !run.attempted || f.sendCalls != 0 {
				t.Fatalf("capture/save state wrong err=%v", err)
			}
			if changed {
				if err == nil || f.closeCalls != 0 {
					t.Fatalf("changed save closed owned backend: err=%v closes=%d", err, f.closeCalls)
				}
			} else if err != nil || f.closeCalls != 1 || !run.savedDraftBodyMatches("\n\u2028"+input.Body) {
				t.Fatalf("save mapping failed: err=%v closes=%d", err, f.closeCalls)
			}
		})
	}
}
