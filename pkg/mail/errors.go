package mail

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is the sentinel every lookup failure unwraps to, so callers can
// classify with errors.Is without matching message text.
var ErrNotFound = errors.New("not found")

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
