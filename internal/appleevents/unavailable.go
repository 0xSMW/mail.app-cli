//go:build !darwin || !cgo

package appleevents

import "context"

func Available() bool { return false }
func Send(ctx context.Context, event Event) (Value, Stats, error) {
	if err := validateEvent(event); err != nil {
		return Value{}, Stats{}, err
	}
	if err := ctx.Err(); err != nil {
		return Value{}, Stats{}, err
	}
	return Value{}, Stats{}, ErrUnavailable
}
func RoundTrip(value Value) (Value, error) {
	if err := validateValue(value, 0); err != nil {
		return Value{}, err
	}
	return Value{}, ErrUnavailable
}
