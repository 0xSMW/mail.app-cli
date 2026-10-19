package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	for _, scenario := range []string{"complete", "missing", "wrong bytes", "extra file", "corrupt encoding", "truncated", "placeholder", "ambiguous", "account mismatch", "cancelled", "input changed", "compose changed", "false acknowledgement", "lost acknowledgement"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			if scenario == "false acknowledgement" {
				f.sendReply = ae.Bool(false)
			}
			if scenario == "lost acknowledgement" {
				f.sendErr = &ae.Error{Code: -1712, Attempted: true, Message: "lost send acknowledgement"}
			}
			input := fixtureOutgoingInput()
			input.Body = "Multiline café 日本語 🚀\nLiteral <angle> & symbols.\n"
			names := []string{"résumé 世界.txt", "second.bin"}
			payloads := [][]byte{[]byte("first café 日本語\n"), {0, 1, 2, 255, 4}}
			for i, name := range names {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, payloads[i], 0600); err != nil {
					t.Fatal(err)
				}
				input.Attachments = append(input.Attachments, path)
			}
			source := attachmentProofMIME(names, payloads)
			switch scenario {
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
				if descriptorClass(container) == "mssg" {
					if prop == "ID  " {
						if container.Fields["form"].Code == "ID  " {
							return container.Fields["seld"], nil
						}
						if !saved {
							return ae.List(), nil
						}
						if scenario == "ambiguous" {
							return ae.List(ae.Int(15), ae.Int(16)), nil
						}
						return ae.List(ae.Int(15)), nil
					}
					if prop == "mbxp" {
						if scenario == "account mismatch" {
							return ruleFixtureRef("foreign-box"), nil
						}
						return box, nil
					}
					if prop == "raso" {
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
					if scenario == "cancelled" {
						cancel()
					}
					if scenario == "input changed" {
						if err := os.WriteFile(input.Attachments[0], []byte("changed"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "compose changed" {
						f.fields["ctnt"] = ae.String("changed")
					}
					return ae.Null(), ae.Stats{Events: 1, Attempted: true}, nil
				}
				if event.Class+event.ID == "coreclos" && scenario == "complete" && f.sendCalls == 0 {
					t.Fatal("closed composition before send")
				}
				if event.Class+event.ID == "emsgsend" && sourceReads == 0 {
					t.Fatal("sent before raw MIME payload proof")
				}
				return f.send(eventCtx, event)
			}
			result, err := c.SendOutgoing(input)
			if scenario == "complete" {
				if err != nil || !result.Submitted || f.sendCalls != 1 || saveCalls != 1 || f.closeCalls != 1 {
					t.Fatalf("result=%+v err=%v sends=%d saves=%d closes=%d", result, err, f.sendCalls, saveCalls, f.closeCalls)
				}
			} else if scenario == "false acknowledgement" || scenario == "lost acknowledgement" {
				var mutation *MutationError
				if !errors.As(err, &mutation) || !mutation.Attempted || !mutation.Uncertain || mutation.Submitted || !mutation.Saved || result.Submitted || f.sendCalls != 1 || f.closeCalls != 0 {
					t.Fatalf("uncertain attached send result=%+v err=%v sends=%d closes=%d", result, err, f.sendCalls, f.closeCalls)
				}
			} else {
				var mutation *MutationError
				if !errors.As(err, &mutation) || mutation.Attempted || mutation.Uncertain || !mutation.Saved || err == nil || result.Submitted || f.sendCalls != 0 {
					t.Fatalf("unsafe preflight scenario=%s result=%+v err=%v sends=%d", scenario, result, err, f.sendCalls)
				}
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
