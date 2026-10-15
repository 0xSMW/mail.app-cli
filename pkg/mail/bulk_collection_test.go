package mail

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestMailCommandCollectionPreservesCompletionOrderAndErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release := make(chan struct{})
	failure := errors.New("third request failed")
	var values []int
	failures := collectMailCommands([]int{0, 1, 2}, func(value int) (int, error) {
		if value == 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		if value == 2 {
			return 0, failure
		}
		return value, nil
	}, func(value int) {
		values = append(values, value)
		if value == 1 {
			close(release)
		}
	})
	if !reflect.DeepEqual(values, []int{1, 0}) || len(failures) != 1 || failures[0] != failure {
		t.Fatalf("values=%v errors=%v", values, failures)
	}
}

func TestMailCommandCollectionKeepsFourCommandLimit(t *testing.T) {
	var active, peak, started atomic.Int32
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failures := collectMailCommands(make([]int, 12), func(int) (struct{}, error) {
		count := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); count > previous && !peak.CompareAndSwap(previous, count); previous = peak.Load() {
		}
		if started.Add(1) == maxConcurrentMailCommands {
			close(release)
		}
		select {
		case <-release:
			return struct{}{}, nil
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		}
	}, func(struct{}) {})
	if len(failures) != 0 || peak.Load() != maxConcurrentMailCommands || started.Load() != 12 {
		t.Fatalf("peak=%d started=%d errors=%v", peak.Load(), started.Load(), failures)
	}
}
