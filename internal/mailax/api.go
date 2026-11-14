// Package mailax provides the public Accessibility operations needed to archive
// an individual message in an explicitly owned Mail viewer. It never scripts Mail.
package mailax

import "context"

// Driver is injectable so selection, ownership changes, and uncertain presses can be
// exercised without opening Mail or performing an archive action.
type Driver interface {
	Trusted() error
	Prepare(context.Context, int64) (Session, error)
}

// Session retains only the owned viewer and its message list. SelectNext chooses
// an individual row with the exact subject from refreshed, complete AXRows
// scans within a bounded lookup budget; the caller must separately compare
// the selected message's Apple Event identity before accepting that row.
// beforeStep must verify the owned Mail viewer's account/mailbox and hidden
// preview. It runs before each discovery/dispatch step, including expansion
// and scrolling; discovery alone never authorizes an action.
type Session interface {
	SelectNext(context.Context, string, func() error) (bool, error)
	Guard() error
	ValidateArchive() error
	// Press returns true only after AXPress may have been dispatched. A failure
	// after that point requires reconciliation and must never be retried.
	Press() (attempted bool, err error)
	Release()
}
