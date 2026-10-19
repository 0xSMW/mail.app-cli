//go:build darwin && cgo

package appleevents

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"
)

func TestReadOnlyAccountEventTimingProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_ACCOUNT_TIMING_PROBE") != "1" {
		t.Skip("read-only account timing probe disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	get := func(direct Value) (Value, Stats, int) {
		t.Helper()
		value, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": direct}, Timeout: 5 * time.Second})
		if err == nil {
			return value, stats, 0
		}
		var native *Error
		if errors.As(err, &native) {
			return Value{}, stats, native.Code
		}
		t.Fatalf("account event timing failed: errorType=%T", err)
		return Value{}, stats, 0
	}
	accounts, initialStats, code := get(All("mact", Null()))
	if code != 0 || accounts.Kind != "list" {
		t.Fatalf("account timing initial collection kind=%s code=%d", accounts.Kind, code)
	}
	t.Logf("initialCollection count=%d elapsedMS=%.2f", len(accounts.Items), initialStats.ElapsedMS)
	columnDurations := []float64{}
	bagDurations := []float64{}
	for round := 0; round < 3; round++ {
		totalMS := 0.0
		for _, property := range []string{"pnam", "ID  ", "emad", "unme", "isac", "atyp"} {
			value, stats, code := get(Property(property, All("mact", Null())))
			totalMS += stats.ElapsedMS
			t.Logf("round=%d column=%q kind=%s count=%d nativeCode=%d elapsedMS=%.2f", round, property, value.Kind, len(value.Items), code, stats.ElapsedMS)
		}
		columnDurations = append(columnDurations, totalMS)
		totalMS = 0
		successes, failures := 0, 0
		for i, reference := range accounts.Items {
			value, stats, code := get(Property("pALL", reference))
			totalMS += stats.ElapsedMS
			if code == 0 && value.Kind == "record" {
				successes++
			} else {
				failures++
			}
			t.Logf("round=%d accountIndex=%d bagKind=%s nativeCode=%d elapsedMS=%.2f", round, i, value.Kind, code, stats.ElapsedMS)
		}
		bagDurations = append(bagDurations, totalMS)
		t.Logf("round=%d bulkSixEventMS=%.2f bagEventMS=%.2f bagSuccesses=%d bagFailures=%d", round, columnDurations[len(columnDurations)-1], totalMS, successes, failures)
	}
	sort.Float64s(columnDurations)
	sort.Float64s(bagDurations)
	t.Logf("nativeEventMedian bulkSixColumnsMS=%.2f perAccountBagsMS=%.2f accountCount=%d bagsExcludeCollectionAndUnsupportedFieldFallback=true", columnDurations[1], bagDurations[1], len(accounts.Items))
}
