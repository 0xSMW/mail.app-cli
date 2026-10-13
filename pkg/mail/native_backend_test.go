package mail

import (
	"context"
	"errors"
	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"testing"
	"time"
)

func TestNativeBackendSelection(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	c := NewClient()
	if c.Backend() != BackendLegacy {
		t.Fatal(c.Backend())
	}
	if err := c.SetBackend("native"); err != nil || c.Backend() != BackendNative {
		t.Fatalf("native selection: %v", err)
	}
	if err := c.SetBackend("bogus"); err == nil {
		t.Fatal("invalid backend accepted")
	}
	t.Setenv("MAIL_APP_CLI_BACKEND", "invalid")
	if err := NewClient().SetBackend(""); err == nil {
		t.Fatal("invalid environment backend accepted")
	}
}

func TestNativeTimeoutRetainsDispatchCauseAndTrace(t *testing.T) {
	c := NewClient()
	_ = c.SetBackend("native")
	c.shared.nativeSender = func(context.Context, appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		return appleevents.Value{}, appleevents.Stats{Attempted: true, Events: 1}, &appleevents.Error{Code: -1712, Attempted: true, Cause: context.DeadlineExceeded}
	}
	err := c.withNativeAutomation(time.Second, func(n *nativeMail) error {
		if e := n.phase("resolved", func() error { return nil }); e != nil {
			return e
		}
		return n.phase("dispatch", func() error { _, e := n.get(appleevents.Property("vers", appleevents.Null())); return e })
	})
	var timeout *AutomationTimeoutError
	var transport *appleevents.Error
	if !errors.As(err, &timeout) || !errors.As(err, &transport) || !transport.Attempted || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost dispatched timeout cause: %v", err)
	}
	if timeout.Trace.LastCompleted != "resolved" || timeout.Trace.Pending != "dispatch" {
		t.Fatalf("trace: %+v", timeout.Trace)
	}
}

func TestNativePropertyTypeFailure(t *testing.T) {
	c := NewClient()
	_ = c.SetBackend("native")
	c.shared.nativeSender = func(context.Context, appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		return appleevents.Null(), appleevents.Stats{}, nil
	}
	err := c.withNativeAutomation(time.Second, func(n *nativeMail) error { _, e := n.boolProperty("isac", appleevents.Null()); return e })
	if err == nil {
		t.Fatal("missing value became a false successful property")
	}
}

func TestNativeContentRequestMeasurement(t *testing.T) {
	content := appleevents.Property("ctnt", appleevents.ByID("mssg", appleevents.Int(1), appleevents.Null()))
	if !requestsNativeContent(content) || !requestsNativeContent(appleevents.List(appleevents.Property("subj", appleevents.Null()), content)) {
		t.Fatal("content request was not measured")
	}
	if requestsNativeContent(appleevents.Property("raso", appleevents.Null())) || requestsNativeContent(appleevents.String("ctnt")) {
		t.Fatal("non-rendering source request counted as content")
	}
}
