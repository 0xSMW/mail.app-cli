//go:build darwin && cgo

package appleevents

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestReadOnlyMessageRangeProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_RANGE_PROBE") != "1" {
		t.Skip("read-only ID range probe disabled")
	}
	accountName := os.Getenv("MAIL_AE_PROBE_ACCOUNT")
	if accountName == "" {
		t.Fatal("read-only probe requires explicit MAIL_AE_PROBE_ACCOUNT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mailboxName := os.Getenv("MAIL_AE_PROBE_MAILBOX")
	if mailboxName == "" {
		mailboxName = "INBOX"
	}
	box := ByName("mbxp", mailboxName, ByName("mact", accountName, Null()))
	get := func(label string, direct Value) Value {
		t.Helper()
		value, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": direct}, Timeout: 5 * time.Second})
		if err != nil {
			var native *Error
			if errors.As(err, &native) {
				t.Fatalf("probe=%s nativeCode=%d attempted=%t", label, native.Code, native.Attempted)
			}
			t.Fatalf("probe=%s errorType=%T", label, err)
		}
		t.Logf("probe=%s kind=%s itemCount=%d events=%d elapsedMS=%.2f", label, value.Kind, len(value.Items), stats.Events, stats.ElapsedMS)
		return value
	}
	full := get("full-ID-column", Property("ID  ", All("mssg", box)))
	if full.Kind != "list" {
		t.Fatalf("full ID column kind=%s", full.Kind)
	}
	if len(full.Items) < 2 {
		t.Skip("fixture has fewer than two messages")
	}
	rangeObject := Range("mssg", 1, 2, box)
	bounded := get("range-ID-column", Property("ID  ", rangeObject))
	if bounded.Kind != "list" || len(bounded.Items) != 2 {
		t.Fatalf("bounded ID column kind=%s count=%d", bounded.Kind, len(bounded.Items))
	}
	if !reflect.DeepEqual(bounded.Items, full.Items[:2]) {
		t.Fatal("bounded ID range differs from initial full-list prefix")
	}
	t.Log("range-prefixMatches=true comparedCount=2")
	singleton := get("range-single-ID-column", Property("ID  ", Range("mssg", 1, 1, box)))
	if singleton.Kind != "list" || len(singleton.Items) != 1 {
		t.Fatalf("singleton ID range kind=%s count=%d", singleton.Kind, len(singleton.Items))
	}
	if !reflect.DeepEqual(singleton.Items, full.Items[:1]) {
		t.Fatal("singleton ID range differs from initial full-list prefix")
	}
	t.Log("range-singletonPrefixMatches=true comparedCount=1")

}
