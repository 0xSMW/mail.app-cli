// Package clierr defines the error contract the CLI presents to callers:
// a short machine code, an exit status, a message, and a hint.
package clierr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// Code is the machine-readable category of a failure.
type Code string

const (
	CodeUsage          Code = "usage"
	CodeNotFound       Code = "not_found"
	CodeUnavailable    Code = "unavailable"
	CodeTimeout        Code = "timeout"
	CodePartial        Code = "partial"
	CodeMutationFailed Code = "mutation_failed"
	CodeInternal       Code = "internal"
)

// ExitCode maps a code to the process exit status documented in
// `mail-app-cli help exit-codes`.
func ExitCode(code Code) int {
	switch code {
	case CodeUsage:
		return 1
	case CodeNotFound:
		return 2
	case CodeUnavailable:
		return 3
	case CodeTimeout:
		return 4
	case CodePartial:
		return 5
	case CodeMutationFailed:
		return 6
	default:
		return 7
	}
}

// Table lists every code with its exit status and meaning, in exit order.
func Table() []struct {
	Code    Code
	Exit    int
	Meaning string
} {
	return []struct {
		Code    Code
		Exit    int
		Meaning string
	}{
		{CodeUsage, 1, "bad flags or arguments, missing account or mailbox, refused flag combination"},
		{CodeNotFound, 2, "message, account, mailbox, rule, draft, signature, or attachment does not exist"},
		{CodeUnavailable, 3, "Mail.app is missing, automation permission is denied, or the Envelope Index cannot be read"},
		{CodeTimeout, 4, "a Mail.app automation call or its queue wait exceeded its deadline"},
		{CodePartial, 5, "a cross-mailbox search was incomplete and --allow-partial was not set"},
		{CodeMutationFailed, 6, "one or more requested mutations failed or could not be verified"},
		{CodeInternal, 7, "anything else"},
	}
}

// Error is the CLI-facing failure. Cause keeps the original error for %w.
type Error struct {
	Code    Code
	Message string
	Hint    string
	Cause   error
	// Reported is set once the error has been written inside a result
	// envelope, so the process exits with its code without printing again.
	Reported bool
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Cause }

// New builds an error with a code and message.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap attaches a code and message to an underlying error.
func Wrap(code Code, err error, message string) *Error {
	return &Error{Code: code, Message: message, Cause: err}
}

// WithHint returns a copy carrying a remediation hint.
func (e *Error) WithHint(hint string) *Error {
	copy := *e
	copy.Hint = hint
	return &copy
}

// Usage builds a usage error.
func Usage(message string) *Error {
	return New(CodeUsage, message)
}

// Usagef builds a formatted usage error.
func Usagef(format string, args ...any) *Error {
	return New(CodeUsage, fmt.Sprintf(format, args...))
}

// Classify converts any error into an *Error. Typed errors from pkg/mail map
// by type; the automation bridge's string errors map by content.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}

	var timeout *mail.AutomationTimeoutError
	var lockTimeout *mail.AutomationLockTimeoutError
	var partial *mail.PartialSearchError
	var notFound *mail.NotFoundError
	switch {
	case errors.As(err, &lockTimeout):
		return Wrap(CodeTimeout, err, err.Error()).WithHint("another mail-app-cli command is holding Mail.app; wait for it or rerun")
	case errors.As(err, &timeout):
		return Wrap(CodeTimeout, err, err.Error()).WithHint("Mail.app did not answer in time; narrow the request with --limit or a smaller mailbox")
	case errors.As(err, &partial):
		return Wrap(CodePartial, err, err.Error()).WithHint("rerun with --allow-partial to accept incomplete results, or scope with --account and --mailbox")
	case errors.As(err, &notFound):
		return Wrap(CodeNotFound, err, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(CodeTimeout, err, err.Error())
	}

	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	// A missing executable is an environment problem even though its text
	// contains "not found". It must take precedence over the bridge's broad
	// text-based mail.IsNotFound fallback.
	case strings.Contains(lower, "executable file not found"):
		return Wrap(CodeUnavailable, err, text).WithHint("run 'mail-app-cli doctor' to see which Mail.app access is missing")
	// The automation bridge can attach the generic AppleScript -2700 code to
	// an otherwise specific lookup failure. Check that signal first so it does
	// not mask a missing message, mailbox, or other resource.
	case mail.IsNotFound(err):
		return Wrap(CodeNotFound, err, text)
	case containsAny(lower,
		"not authorized to send apple events",
		"-1743",
		"application can't be found",
		"-600)",
		"-2700)",
		"authorization denied",
		"operation not permitted",
		"full disk access",
		"envelope index"):
		return Wrap(CodeUnavailable, err, text).WithHint("run 'mail-app-cli doctor' to see which Mail.app access is missing")
	}
	return Wrap(CodeInternal, err, text)
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
