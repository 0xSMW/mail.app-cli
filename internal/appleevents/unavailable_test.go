//go:build !darwin || !cgo

package appleevents

import (
	"context"
	"errors"
	"testing"
)

func TestUnavailableNativeMode(t *testing.T) {
	if Available() {
		t.Fatal("native backend reported available")
	}
	if _, err := RoundTrip(String("text")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("roundtrip: %v", err)
	}
	_, stats, err := Send(context.Background(), Event{Class: "core", ID: "getd"})
	if !errors.Is(err, ErrUnavailable) || stats.Attempted || stats.Events != 0 {
		t.Fatalf("send stats=%+v err=%v", stats, err)
	}
}
