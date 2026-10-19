package mailax

import (
	"context"
	"fmt"
	"time"
)

// prepareOwnedFocus waits for activation before requesting window focus once.
// A successful request still needs two exact owned-window observations; merely
// making Mail foreground or accepting a setter never authorizes an Archive press.
func prepareOwnedFocus(ctx context.Context, request func() (bool, error), read func() (bool, error), deadline time.Time, interval time.Duration) error {
	requested := false
	return waitForOwnedFocus(ctx, func() (bool, error) {
		if !requested {
			var err error
			requested, err = request()
			if err != nil {
				return false, err
			}
			if !requested {
				return false, nil
			}
		}
		return read()
	}, deadline, interval)
}

// waitForOwnedFocus requires two consecutive observations of the exact owned
// viewer. Activation is asynchronous; another Mail window being focused never
// satisfies this check. The caller supplies the remaining preparation budget.
func waitForOwnedFocus(ctx context.Context, read func() (bool, error), deadline time.Time, interval time.Duration) error {
	stable := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("owned Mail viewer did not become stably focused")
		}
		owned, err := read()
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("owned Mail viewer did not become stably focused")
		}
		if owned && stable {
			return nil
		}
		stable = owned
		remaining = time.Until(deadline)
		pause := min(interval, remaining)
		if pause <= 0 {
			return fmt.Errorf("owned Mail viewer did not become stably focused")
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
