package mailax

import (
	"context"
	"fmt"
	"time"
)

// prepareOwnedViewer retries only the absence of the exact owned window/table.
// It never activates an application or changes a window's focus. Each attempt
// receives the remaining budget; all failed or interrupted sessions are released.
func prepareOwnedViewer(ctx context.Context, attempt func(time.Duration) (Session, bool, error), deadline time.Time, interval time.Duration) (Session, error) {
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("owned Mail viewer did not become ready")
		}
		session, retryable, err := attempt(remaining)
		if canceled := ctx.Err(); canceled != nil {
			if session != nil {
				session.Release()
			}
			return nil, canceled
		}
		if err == nil && session != nil && time.Now().Before(deadline) {
			return session, nil
		}
		if session != nil {
			session.Release()
		}
		if err == nil {
			err = fmt.Errorf("owned Mail viewer did not become ready")
		}
		if !retryable || !time.Now().Before(deadline) {
			return nil, err
		}
		pause := min(interval, time.Until(deadline))
		if pause <= 0 {
			return nil, err
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
