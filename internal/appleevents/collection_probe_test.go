//go:build darwin && cgo

package appleevents

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// This explicitly enabled integration probe reads account collection/property
// descriptors only. It logs types and counts, never names, identifiers, or
// addresses, and performs no mutations or message reads.
func TestReadOnlyCollectionProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_COLLECTION_PROBE") != "1" {
		t.Skip("read-only collection integration probe disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	get := func(object Value) Value {
		t.Helper()
		value, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": object}, Timeout: 5 * time.Second})
		if err != nil {
			var native *Error
			if errors.As(err, &native) {
				t.Fatalf("read-only collection/property probe failed: native code=%d attempted=%t", native.Code, native.Attempted)
			}
			t.Fatalf("read-only collection/property probe failed: error type=%T", err)
		}
		if !stats.Attempted || stats.Events != 1 {
			t.Fatalf("unexpected probe dispatch stats: %+v", stats)
		}
		return value
	}
	accounts := get(All("mact", Null()))
	if accounts.Kind != "list" {
		t.Fatalf("account collection result kind=%s", accounts.Kind)
	}
	t.Logf("native account collection count=%d", len(accounts.Items))
	if len(accounts.Items) == 0 {
		return
	}
	first := get(Index("mact", 1, Null()))
	if first.Kind != "opaque" || first.Code != "obj " {
		t.Fatalf("indexed account result kind=%s code=%s", first.Kind, first.Code)
	}
	for _, property := range []struct{ code, kind string }{{"ID  ", "string"}, {"pnam", "string"}, {"atyp", "enum"}, {"emad", "list"}} {
		value := get(Property(property.code, accounts.Items[0]))
		if value.Kind != property.kind {
			t.Fatalf("property code=%s result kind=%s", property.code, value.Kind)
		}
		t.Logf("native property code=%s result kind=%s", property.code, value.Kind)
	}
}
