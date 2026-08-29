package mail

import "fmt"

// CapabilityStatus describes whether a Mail feature can be supplied by the
// backends this CLI is allowed to use.
type CapabilityStatus string

const (
	CapabilityUnavailable CapabilityStatus = "unavailable"
	CapabilityUnsupported CapabilityStatus = "unsupported"
)

// CapabilityError keeps a feature limitation distinct from an empty result.
// Callers should present this as a capability failure, never as an empty list.
type CapabilityError struct {
	Capability string
	Status     CapabilityStatus
	Cause      error
}

func (e *CapabilityError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s is %s: %v", e.Capability, e.Status, e.Cause)
	}
	return fmt.Sprintf("%s is %s", e.Capability, e.Status)
}

func (e *CapabilityError) Unwrap() error { return e.Cause }
