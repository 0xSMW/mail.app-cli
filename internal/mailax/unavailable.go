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
func (d unavailableDriver) FrontApplication() (string, error)               { return "", d.Trusted() }
func (d unavailableDriver) Prepare(context.Context, int64) (Session, error) { return nil, d.Trusted() }
func (d unavailableDriver) Restore(string) error                            { return d.Trusted() }
func (d unavailableDriver) OwnedFocus(int64) (bool, error)                  { return false, d.Trusted() }
