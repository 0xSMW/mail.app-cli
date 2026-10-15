package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// Only exact bridge-owned diagnostics are safe to print. Mail's dynamic error
// text can include private identifiers or message values, so it is withheld.
func nativeReferenceProbeSafeError(err error) string {
	var ae *appleevents.Error
	if !errors.As(err, &ae) {
		return ""
	}
	switch ae.Message {
	case "Mail PID target probe does not identify a running com.apple.mail instance",
		"Mail PID target probe only permits core/getd and core/cnte with a positive PID",
		"Mail PID target probe instance changed before dispatch",
		"Invalid Apple Event bridge input",
		"Apple Event codes require four ASCII bytes",
		"Apple Event codes require printable ASCII bytes",
		"Invalid flattened Apple Event descriptor",
		"Cannot flatten Apple Event descriptor",
		"Apple Event descriptor nesting exceeds 128",
		"Apple Event reply nesting exceeds 128",
		"Cannot create Apple Event object specifier",
		"Cannot create Apple Event range descriptor",
		"Unsupported Apple Event descriptor kind",
		"Mail automation permission is unavailable; no consent prompt was requested",
		"Apple Event deadline expired before dispatch",
		"Mail Apple Event timed out",
		"Mail Apple Event dispatch failed",
		"Cannot encode native Apple Event response":
		return ae.Message
	}
	return ""
}

// This explicit opt-in probe reads public metadata only. Its output contains
// timings, shapes, counts and equality results; no identifiers or mail values.
// Production routing remains unchanged until the caller measures these shapes.
func TestNativeMessageReferenceMetadataProbe(t *testing.T) {
	if os.Getenv("MAIL_APP_CLI_NATIVE_REFERENCE_PROBE") != "1" {
		t.Skip("native metadata reference probe is opt-in")
	}
	account := os.Getenv("MAIL_APP_CLI_NATIVE_REFERENCE_PROBE_ACCOUNT")
	mailbox := os.Getenv("MAIL_APP_CLI_NATIVE_REFERENCE_PROBE_MAILBOX")
	if account == "" || mailbox == "" {
		t.Fatal("reference probe requires explicit account and mailbox environment inputs")
	}
	c := NewClient()
	if err := c.SetBackend(BackendNative); err != nil {
		t.Fatal(err)
	}
	before := BackendStatistics()
	stage := "automation_lock"
	err := c.withNativeAutomation(55*time.Second, func(n *nativeMail) error {
		type sample struct {
			Label       string  `json:"label"`
			Kind        string  `json:"kind"`
			Count       int     `json:"count"`
			ElapsedMS   float64 `json:"elapsedMs"`
			Events      int64   `json:"events"`
			NativeCode  int     `json:"nativeCode,omitempty"`
			ErrorType   string  `json:"errorType,omitempty"`
			SafeMessage string  `json:"safeMessage,omitempty"`
		}
		emit := func(row sample) { encoded, _ := json.Marshal(row); t.Logf("reference-probe %s", encoded) }
		measure := func(label string, fn func() (appleevents.Value, error)) (appleevents.Value, error) {
			stage = label
			started := time.Now()
			stats := BackendStatistics()
			value, err := fn()
			count := 1
			if value.Kind == "list" {
				count = len(value.Items)
			}
			if value.Kind == "" || value.Kind == "null" {
				count = 0
			}
			row := sample{Label: label, Kind: value.Kind, Count: count, ElapsedMS: float64(time.Since(started).Nanoseconds()) / 1e6, Events: BackendStatistics().NativeEvents - stats.NativeEvents}
			if err != nil {
				var ae *appleevents.Error
				if errors.As(err, &ae) {
					row.NativeCode = ae.Code
				}
				row.ErrorType = fmt.Sprintf("%T", err)
				row.SafeMessage = nativeReferenceProbeSafeError(err)
			}
			emit(row)
			return value, err
		}
		stage = "resolve_account"
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		stage = "resolve_mailbox"
		box, err := n.mailbox(acc, mailbox)
		if err != nil {
			return err
		}
		ids, err := measure("source_id_column", func() (appleevents.Value, error) {
			return n.get(appleevents.Property("ID  ", appleevents.All("mssg", box)))
		})
		if err != nil {
			return err
		}
		if ids.Kind != "list" {
			return fmt.Errorf("source ID reply is not a list")
		}
		if len(ids.Items) == 0 {
			t.Log("reference-probe sourceCount=0; no metadata samples")
			return nil
		}
		allRefs, allErr := measure("resolve_all_message_refs", func() (appleevents.Value, error) { return n.get(appleevents.All("mssg", box)) })
		if allErr == nil && (allRefs.Kind != "list" || len(allRefs.Items) != len(ids.Items)) {
			return fmt.Errorf("source reference collection changed or has an unexpected shape")
		}
		// The collection-column shape mirrors the existing JXA bulk access.
		// It is a diagnostic here, not a production listing fallback.
		for _, code := range nativeListMessageCodes {
			values, err := measure("full_collection_column_"+code, func() (appleevents.Value, error) {
				return n.get(appleevents.Property(code, appleevents.All("mssg", box)))
			})
			if err != nil {
				return err
			}
			if values.Kind != "list" || len(values.Items) != len(ids.Items) {
				return fmt.Errorf("full collection metadata reply changed or has an unexpected shape")
			}
		}
		for _, requested := range []int{1, 25} {
			count := requested
			if count > len(ids.Items) {
				count = len(ids.Items)
			}
			expected := ids.Items[:count]
			for _, id := range expected {
				if id.Kind != "int" || id.Number <= 0 {
					return fmt.Errorf("source ID has an unexpected shape")
				}
			}
			prefix := fmt.Sprintf("sample_%d", count)
			bounded := appleevents.Range("mssg", 1, count, box)
			rangeRefs, rangeErr := measure(prefix+"_resolve_range_refs", func() (appleevents.Value, error) { return n.get(bounded) })
			candidates := map[string][]appleevents.Value{}
			if rangeErr == nil {
				if rangeRefs.Kind != "list" || len(rangeRefs.Items) != count {
					return fmt.Errorf("resolved range has an unexpected shape")
				}
				candidates["returned_range_refs"] = rangeRefs.Items
			}
			if allErr == nil {
				candidates["returned_all_refs"] = allRefs.Items[:count]
			}
			scoped := make([]appleevents.Value, count)
			for i, id := range expected {
				scoped[i] = appleevents.ByID("mssg", id, box)
			}
			candidates["scoped_by_id_specs"] = scoped
			resolvedByID, resolvedErr := measure(prefix+"_resolve_scoped_ids", func() (appleevents.Value, error) {
				resolved := make([]appleevents.Value, count)
				for i, spec := range scoped {
					value, err := n.get(spec)
					if err != nil {
						return appleevents.Value{}, err
					}
					resolved[i] = value
				}
				return appleevents.List(resolved...), nil
			})
			if resolvedErr == nil {
				candidates["returned_by_id_refs"] = resolvedByID.Items
			}
			for label, refs := range candidates {
				for _, ref := range refs {
					if ref.Kind != "object" && ref.Kind != "opaque" {
						return fmt.Errorf("resolved message reference has an unexpected shape")
					}
				}
				for _, code := range nativeListMessageCodes {
					values, err := measure(prefix+"_"+label+"_"+code, func() (appleevents.Value, error) {
						out := make([]appleevents.Value, len(refs))
						for i, ref := range refs {
							value, err := n.get(appleevents.Property(code, ref))
							if err != nil {
								return appleevents.Value{}, err
							}
							out[i] = value
						}
						return appleevents.List(out...), nil
					})
					if err != nil {
						return err
					}
					if code == "ID  " {
						for i, id := range expected {
							if values.Items[i].Kind != "int" || values.Items[i].Number != id.Number {
								return fmt.Errorf("resolved reference ID changed")
							}
						}
					}
				}
				postIDs, err := measure(prefix+"_"+label+"_post_reference_ID", func() (appleevents.Value, error) {
					out := make([]appleevents.Value, len(refs))
					for i, ref := range refs {
						value, err := n.get(appleevents.Property("ID  ", ref))
						if err != nil {
							return appleevents.Value{}, err
						}
						out[i] = value
					}
					return appleevents.List(out...), nil
				})
				if err != nil {
					return err
				}
				for i, id := range expected {
					if postIDs.Items[i].Kind != "int" || postIDs.Items[i].Number != id.Number {
						return fmt.Errorf("resolved reference ID changed after metadata")
					}
				}
				// A literal list of returned refs might support a bulk property read;
				// a typed unsupported-read result is diagnostic rather than a fallback.
				if label != "scoped_by_id_specs" {
					_, _ = measure(prefix+"_"+label+"_literal_list_subjects", func() (appleevents.Value, error) {
						return n.get(appleevents.Property("subj", appleevents.List(refs...)))
					})
				}
				propertyRefs := make([]appleevents.Value, len(refs))
				for i, ref := range refs {
					propertyRefs[i] = appleevents.Property("subj", ref)
				}
				for _, shape := range []struct {
					name          string
					direct        appleevents.Value
					requestedType bool
				}{
					{"batched_property_specs", appleevents.List(propertyRefs...), false},
					{"batched_property_specs_requested_list", appleevents.List(propertyRefs...), true},
					{"literal_ref_list_requested_list", appleevents.Property("subj", appleevents.List(refs...)), true},
				} {
					_, err := measure(prefix+"_"+label+"_"+shape.name, func() (appleevents.Value, error) {
						params := map[string]appleevents.Value{"----": shape.direct}
						if shape.requestedType {
							params["rtyp"] = appleevents.TypeCode("list")
						}
						return n.event("core", "getd", params)
					})
					if err != nil && !nativeUnsupportedRead(err) {
						return err
					}
				}
				stage = prefix + "_" + label + "_verify_scoped_membership"
				final, err := n.get(appleevents.Property("ID  ", bounded))
				if err != nil {
					return err
				}
				if final.Kind != "list" || len(final.Items) != count {
					return fmt.Errorf("scoped range membership changed")
				}
				for i, id := range expected {
					if final.Items[i].Kind != "int" || final.Items[i].Number != id.Number {
						return fmt.Errorf("scoped range ID order changed")
					}
				}
				t.Logf("reference-probe sample=%d mode=%s scopedIDsMatched=true", count, label)
			}
			for _, code := range nativeListMessageCodes {
				_, err := measure(prefix+"_range_projection_"+code, func() (appleevents.Value, error) { return n.get(appleevents.Property(code, bounded)) })
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	after := BackendStatistics()
	if after.NativeContentRequests != before.NativeContentRequests || after.LegacyProcesses != before.LegacyProcesses {
		t.Fatal("metadata probe requested content or invoked legacy automation")
	}
	if err != nil {
		var ae *appleevents.Error
		code := 0
		if errors.As(err, &ae) {
			code = ae.Code
		}
		t.Fatalf("reference probe failed: stage=%s errorType=%T nativeCode=%d safeMessage=%q", stage, err, code, nativeReferenceProbeSafeError(err))
	}
}

func TestNativeReadProbeRedactsDynamicErrors(t *testing.T) {
	known := &appleevents.Error{Code: -50, Message: "Mail PID target probe only permits core/getd and core/cnte with a positive PID"}
	if nativeReferenceProbeSafeError(known) != known.Message {
		t.Fatal("bridge diagnostic was hidden")
	}
	private := &appleevents.Error{Code: -50, Message: "Mail cannot get subject of message id 424242 in a private account"}
	if nativeReferenceProbeSafeError(private) != "" {
		t.Fatal("dynamic Mail error was exposed")
	}
	if nativeReferenceProbeSafeError(errors.New("private data")) != "" {
		t.Fatal("unknown error text was exposed")
	}
}
