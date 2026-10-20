//go:build !darwin || !cgo

package mailax

import (
	"context"
	"fmt"
)

type unavailableDriver struct{}

func New() Driver { return unavailableDriver{} }
func (unavailableDriver) Trusted() error {
	return fmt.Errorf("native Mail Accessibility requires macOS with cgo")
}
func (d unavailableDriver) Prepare(context.Context, int64) (Session, error) { return nil, d.Trusted() }
