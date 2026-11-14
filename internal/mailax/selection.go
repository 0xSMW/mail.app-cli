package mailax

import (
	"context"
	"errors"
	"time"
)

// pollOwnedRows refreshes exhausted AX scans within one session-wide budget.
// A scan may expose a new target after Mail finishes populating its table.
func pollOwnedRows(ctx context.Context, deadline time.Time, interval time.Duration, scan func(time.Duration) (bool, error)) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		found, err := scan(remaining)
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if found {
			return time.Now().Before(deadline), nil
		}
		pause := min(interval, time.Until(deadline))
		if pause <= 0 {
			return false, nil
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

var errTraversalExhausted = errors.New("owned Mail traversal exhausted")

// The Mail scope check can itself wait on Apple Events. Recompute the budget
// afterwards so expiry/cancellation cannot authorize a subsequent AX mutation.
func scanAfterGuard(ctx context.Context, deadline time.Time, before func() error, scan func(time.Duration) (bool, error)) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := before(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	remaining := time.Until(deadline)
	if end, ok := ctx.Deadline(); ok {
		remaining = min(remaining, time.Until(end))
	}
	if remaining <= 0 {
		return false, errTraversalExhausted
	}
	return scan(remaining)
}
