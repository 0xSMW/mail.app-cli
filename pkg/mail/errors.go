package mail

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is the sentinel every lookup failure unwraps to, so callers can
// classify with errors.Is without matching message text.
var ErrNotFound = errors.New("not found")

// MutationError records the stage of an outgoing operation. Once submission
// starts, a transport error cannot establish that retrying is safe.
type MutationError struct {
	Operation string
	Phase     string
	Account   string
	Sender    string
	Cleanup   string
	Cause     error
	Attempted bool
	Uncertain bool
	Submitted bool
	Saved     bool
}

func (e *MutationError) Error() string {
	message := fmt.Sprintf("%s failed during %s", e.Operation, e.Phase)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	if e.Uncertain {
		message += "; outcome uncertain; do not automatically retry"
	} else if e.Submitted {
		message += "; already submitted; do not resend"
	} else if e.Saved {
		message += "; draft was saved; do not blindly recreate"
	}
	if e.Cleanup != "" {
		message += "; cleanup failed: " + e.Cleanup
	}
	return message
}

func (e *MutationError) Unwrap() error { return e.Cause }

// NotFoundError names the kind of object that was missing and the selector
// that was used, for example ("message", "123") or ("account", "Work").
type NotFoundError struct {
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	if e.Name == "" {
		return e.Kind + " not found"
	}
	return fmt.Sprintf("%s not found: %s", e.Kind, e.Name)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

func notFound(kind, name string) error {
	return &NotFoundError{Kind: kind, Name: name}
}

// IsNotFound reports whether err is a typed lookup failure or one of the
// automation bridge's "Error: ... not found" strings.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// bridgeError converts an "Error: ..." string returned by an AppleScript or
// JXA script into a Go error, typing the not-found case.
func bridgeError(output string) error {
	output = strings.TrimSpace(output)
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "message not found"):
		return &NotFoundError{Kind: "message", Name: ""}
	case strings.Contains(lower, "draft not found"):
		return &NotFoundError{Kind: "draft", Name: ""}
	case strings.Contains(lower, "mailbox not found"):
		return &NotFoundError{Kind: "mailbox", Name: strings.TrimSpace(strings.TrimPrefix(output, "Error: Mailbox not found:"))}
	case strings.Contains(lower, "attachment not found"):
		return &NotFoundError{Kind: "attachment", Name: ""}
	}
	return errors.New(output)
}
