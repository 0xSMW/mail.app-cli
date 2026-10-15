package mailax

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOwnedFocusSettlesOnlyOnExactStableViewer(t *testing.T) {
	for _, tt := range []struct {
		name         string
		observations []bool
		wantError    bool
		wantReads    int
	}{
		{name: "already-owned", observations: []bool{true, true}, wantReads: 2},
		{name: "eventual-owned", observations: []bool{false, false, true, true}, wantReads: 4},
		{name: "unstable-then-owned", observations: []bool{true, false, true, true}, wantReads: 4},
		{name: "another-mail-window", observations: []bool{false}, wantError: true},
		{name: "no-owned-window", observations: []bool{false}, wantError: true},
		{name: "ownership-flickers", observations: []bool{true, false}, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			read := func() (bool, error) {
				result := tt.observations[reads%len(tt.observations)]
				reads++
				return result, nil
			}
			err := waitForOwnedFocus(context.Background(), read, time.Now().Add(20*time.Millisecond), time.Millisecond)
			if (err != nil) != tt.wantError {
				t.Fatalf("focus wait=%v", err)
			}
			if tt.wantReads != 0 && reads != tt.wantReads {
				t.Fatalf("focus reads=%d want=%d", reads, tt.wantReads)
			}
		})
	}
}

func TestOwnedFocusWaitCancellationAndFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	read := func() (bool, error) { cancel(); return false, nil }
	if err := waitForOwnedFocus(ctx, read, time.Now().Add(time.Second), time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	failure := errors.New("owned window is unavailable")
	if err := waitForOwnedFocus(context.Background(), func() (bool, error) { return false, failure }, time.Now().Add(time.Second), time.Millisecond); !errors.Is(err, failure) {
		t.Fatalf("focus read error=%v", err)
	}
}

func TestOwnedFocusPreparationRequestsExactFocusAfterActivation(t *testing.T) {
	requests, reads := 0, 0
	request := func() (bool, error) { requests++; return requests >= 3, nil }
	read := func() (bool, error) { reads++; return reads >= 2, nil }
	if err := prepareOwnedFocus(context.Background(), request, read, time.Now().Add(50*time.Millisecond), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests != 3 || reads != 3 {
		t.Fatalf("activation/focus requests=%d owned observations=%d", requests, reads)
	}
}

func TestOwnedFocusPreparationNeverAcceptsOtherWindowOrSetterFailure(t *testing.T) {
	requests, reads := 0, 0
	err := prepareOwnedFocus(context.Background(), func() (bool, error) { requests++; return true, nil }, func() (bool, error) { reads++; return false, nil }, time.Now().Add(20*time.Millisecond), time.Millisecond)
	if err == nil || requests != 1 || reads == 0 {
		t.Fatalf("another window accepted: error=%v requests=%d observations=%d", err, requests, reads)
	}
	failure := errors.New("owned AXMain setter failed")
	reads = 0
	err = prepareOwnedFocus(context.Background(), func() (bool, error) { return false, failure }, func() (bool, error) { reads++; return true, nil }, time.Now().Add(time.Second), time.Millisecond)
	if !errors.Is(err, failure) || reads != 0 {
		t.Fatalf("setter failure accepted: error=%v observations=%d", err, reads)
	}
}

func TestOwnedFocusPreparationDoesNotTouchWindowsBeforeMailActivation(t *testing.T) {
	reads := 0
	err := prepareOwnedFocus(context.Background(), func() (bool, error) { return false, nil }, func() (bool, error) { reads++; return true, nil }, time.Now().Add(20*time.Millisecond), time.Millisecond)
	if err == nil || reads != 0 {
		t.Fatalf("inactive Mail preparation accepted: error=%v observations=%d", err, reads)
	}
}
