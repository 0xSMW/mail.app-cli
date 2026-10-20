package mailax

import (
	"context"
	"errors"
	"testing"
	"time"
)

type readinessSession struct{ released int }

func (*readinessSession) SelectNext(context.Context, string) (bool, error) { return false, nil }
func (*readinessSession) Guard() error                                     { return nil }
func (*readinessSession) ValidateArchive() error                           { return nil }
func (*readinessSession) Press() (bool, error)                             { return false, nil }
func (s *readinessSession) Release()                                       { s.released++ }

func TestOwnedViewerReadinessRetriesOnlyUnavailableTree(t *testing.T) {
	waiting, ready := &readinessSession{}, &readinessSession{}
	attempts := 0
	got, err := prepareOwnedViewer(context.Background(), func(budget time.Duration) (Session, bool, error) {
		attempts++
		if budget <= 0 {
			t.Fatal("attempt received no remaining budget")
		}
		if attempts == 1 {
			return waiting, true, errors.New("owned table not ready")
		}
		return ready, false, nil
	}, time.Now().Add(time.Second), time.Millisecond)
	if err != nil || got != ready || attempts != 2 || waiting.released != 1 || ready.released != 0 {
		t.Fatalf("session=%v error=%v attempts=%d releases=%d/%d", got, err, attempts, waiting.released, ready.released)
	}
}

func TestOwnedViewerReadinessFailsClosedOnAmbiguousTree(t *testing.T) {
	session := &readinessSession{}
	failure := errors.New("ambiguous owned window")
	attempts := 0
	got, err := prepareOwnedViewer(context.Background(), func(time.Duration) (Session, bool, error) {
		attempts++
		return session, false, failure
	}, time.Now().Add(time.Second), time.Millisecond)
	if got != nil || !errors.Is(err, failure) || attempts != 1 || session.released != 1 {
		t.Fatalf("session=%v error=%v attempts=%d releases=%d", got, err, attempts, session.released)
	}
}

func TestOwnedViewerReadinessCancellationReleasesPreparedSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &readinessSession{}
	got, err := prepareOwnedViewer(ctx, func(time.Duration) (Session, bool, error) {
		cancel()
		return session, false, nil
	}, time.Now().Add(time.Second), time.Millisecond)
	if got != nil || !errors.Is(err, context.Canceled) || session.released != 1 {
		t.Fatalf("session=%v error=%v releases=%d", got, err, session.released)
	}
}

func TestOwnedViewerReadinessNeverAcceptsLateSuccess(t *testing.T) {
	session := &readinessSession{}
	got, err := prepareOwnedViewer(context.Background(), func(time.Duration) (Session, bool, error) {
		time.Sleep(5 * time.Millisecond)
		return session, false, nil
	}, time.Now().Add(time.Millisecond), time.Millisecond)
	if got != nil || err == nil || session.released != 1 {
		t.Fatalf("session=%v error=%v releases=%d", got, err, session.released)
	}
}

func TestOwnedViewerReadinessExpiredContextDoesNotAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := prepareOwnedViewer(ctx, func(time.Duration) (Session, bool, error) {
		t.Fatal("attempted with canceled context")
		return nil, false, nil
	}, time.Now().Add(time.Second), time.Millisecond)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("session=%v error=%v", got, err)
	}
}
