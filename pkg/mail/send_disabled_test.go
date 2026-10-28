package mail

import (
	"context"
	"errors"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestRestrictedClientBlocksEverySendingEntryPoint(t *testing.T) {
	for _, backend := range []string{BackendNative, BackendLegacy} {
		base := NewClient()
		base.shared.backend = backend
		calls := 0
		base.shared.nativeSender = func(context.Context, ae.Event) (ae.Value, ae.Stats, error) {
			calls++
			return ae.Value{}, ae.Stats{}, errors.New("unexpected transport")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// Cancellation prevents real transport even if a guard regresses.
		c := base.WithSendingDisabled().WithContext(ctx)
		if base.sendingDisabled {
			t.Fatal("restricted copy changed original")
		}
		checks := []struct {
			name string
			run  func() error
		}{
			{"outgoing", func() error { _, err := c.SendOutgoing(OutgoingInput{}); return err }},
			{"message", func() error { return c.SendMessage("", "", "", nil, nil, nil, nil) }},
			{"draft result", func() error { _, err := c.SendDraftResult("", "42"); return err }},
			{"draft", func() error { return c.SendDraft("", "42") }},
		}
		for _, mode := range []ReplyMode{ReplyToSender, ReplyToAll, ForwardMessage} {
			checks = append(checks, struct {
				name string
				run  func() error
			}{string(mode), func() error { _, err := c.SendThreadedOutgoing(ThreadedOutgoingInput{Mode: mode}); return err }})
		}
		for _, check := range checks {
			if err := check.run(); !errors.Is(err, ErrSendingDisabled) {
				t.Errorf("%s/%s: %v", backend, check.name, err)
			}
		}
		if calls != 0 {
			t.Fatalf("contacted Mail %d times", calls)
		}
	}
}
