package mailax

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOwnedRowsRefreshAfterExhaustedSnapshot(t *testing.T) {
	scans := 0
	// The first AXRows snapshot has no target. A later refresh replaces the
	// collection with the requested row, as happens while Mail populates a viewer.
	found, err := pollOwnedRows(context.Background(), time.Now().Add(time.Second), time.Millisecond, func(remaining time.Duration) (bool, error) {
		scans++
		if remaining <= 0 {
			t.Fatal("unbounded scan")
		}
		return scans == 3, nil
	})
	if err != nil || !found || scans != 3 {
		t.Fatalf("found=%v error=%v scans=%d", found, err, scans)
	}
}

func TestOwnedRowsLookupBudgetAndFailures(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		started := time.Now()
		found, err := pollOwnedRows(context.Background(), started.Add(10*time.Millisecond), time.Millisecond, func(time.Duration) (bool, error) { return false, nil })
		if found || err != nil || time.Since(started) > time.Second {
			t.Fatalf("found=%v error=%v", found, err)
		}
	})
	t.Run("ownership error", func(t *testing.T) {
		failure := errors.New("owned table changed")
		scans := 0
		found, err := pollOwnedRows(context.Background(), time.Now().Add(time.Second), time.Millisecond, func(time.Duration) (bool, error) { scans++; return false, failure })
		if found || !errors.Is(err, failure) || scans != 1 {
			t.Fatalf("found=%v error=%v scans=%d", found, err, scans)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		found, err := pollOwnedRows(ctx, time.Now().Add(time.Second), time.Millisecond, func(time.Duration) (bool, error) { cancel(); return true, nil })
		if found || !errors.Is(err, context.Canceled) {
			t.Fatalf("found=%v error=%v", found, err)
		}
	})
}
