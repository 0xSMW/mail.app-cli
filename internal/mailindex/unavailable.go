//go:build !cgo || (!darwin && !linux)

package mailindex

import (
	"context"
	"fmt"
)

func Available() bool { return false }
func Query(context.Context, string, string) ([]byte, error) {
	return nil, fmt.Errorf("embedded Envelope Index reader unavailable on this platform")
}
func Close() {}
func Reset() {}
