//go:build darwin && cgo

package appleevents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

// Logs only public descriptor kinds, field codes, counts, and native statuses.
func TestReadOnlyAccountPropertyBagProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_PROPERTY_BAG_PROBE") != "1" {
		t.Skip("read-only account property bag probe disabled")
	}
	required := []string{"pnam", "ID  ", "emad", "unme", "isac", "atyp"}
	describe := func(label string, value Value) {
		t.Helper()
		t.Logf("probe=%s kind=%s itemCount=%d fieldCount=%d", label, value.Kind, len(value.Items), len(value.Fields))
		values := []Value{value}
		if value.Kind == "list" {
			values = value.Items
		}
		for i, record := range values {
			if record.Kind != "record" {
				t.Logf("probe=%s itemIndex=%d kind=%s", label, i, record.Kind)
				continue
			}
			keys := make([]string, 0, len(record.Fields))
			for key := range record.Fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			t.Logf("probe=%s recordIndex=%d kind=%s fieldCodes=%q", label, i, record.Kind, keys)
			for _, key := range required {
				field, ok := record.Fields[key]
				t.Logf("probe=%s recordIndex=%d requiredCode=%q present=%t kind=%s", label, i, key, ok, field.Kind)
			}
		}
	}
	cases := []struct {
		label         string
		direct        Value
		requestedType string
	}{
		{"all-account-properties", Property("pALL", All("mact", Null())), ""},
		{"single-account-properties", Property("pALL", Index("mact", 1, Null())), ""},
		{"single-account-requested-record", Index("mact", 1, Null()), "reco"},
		{"list-account-property-specifiers", List(Property("pALL", Index("mact", 1, Null())), Property("pALL", Index("mact", 2, Null()))), ""},
		{"list-account-name-specifiers", List(Property("pnam", Index("mact", 1, Null())), Property("pnam", Index("mact", 2, Null()))), ""},
	}
	collectionCtx, collectionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	collection, _, collectionErr := Send(collectionCtx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": All("mact", Null())}})
	collectionCancel()
	if collectionErr == nil && collection.Kind == "list" {
		t.Logf("per-account-property-probe accountCount=%d", len(collection.Items))
		for i, ref := range collection.Items {
			cases = append(cases, struct {
				label         string
				direct        Value
				requestedType string
			}{fmt.Sprintf("reference-account-properties-%d", i), Property("pALL", ref), ""})
		}
	}
	for _, code := range required {
		cases = append(cases, struct {
			label         string
			direct        Value
			requestedType string
		}{"column-" + code, Property(code, All("mact", Null())), ""})
	}
	for _, tc := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		params := map[string]Value{"----": tc.direct}
		if tc.requestedType != "" {
			params["rtyp"] = TypeCode(tc.requestedType)
		}
		value, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: params})
		cancel()
		if err != nil {
			var native *Error
			if errors.As(err, &native) {
				t.Logf("probe=%s nativeCode=%d attempted=%t", tc.label, native.Code, native.Attempted)
			} else {
				t.Logf("probe=%s errorType=%T", tc.label, err)
			}
			continue
		}
		t.Logf("probe=%s eventCount=%d elapsedMS=%.2f", tc.label, stats.Events, stats.ElapsedMS)
		describe(tc.label, value)
	}
}
