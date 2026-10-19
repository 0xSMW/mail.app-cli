package mail

import (
	"context"
	"fmt"
	"sync"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailax"
)

// clientState is shared by every Client derived from the same NewClient call,
// so a WithContext copy sees the same account cache and warning gates.
type clientState struct {
	accountsMu               sync.Mutex
	accounts                 []Account
	accountsLoaded           bool
	nativeArchiveAccounts    map[string]bool
	indexFallbackWarningOnce sync.Once
	contentWarningOnce       sync.Once
	recentCleanupWarningOnce sync.Once
	warn                     func(string)
	bodyFromMail             bool
	bodyAlternatives         bool
	backend                  string
	nativeSender             appleevents.Sender
	nativeAX                 mailax.Driver
}

// Client reads the local index and MIME and sends changes through the selected
// Mail transport. Contexts bound local queries, queued automation and subprocesses.
type Client struct {
	ctx     context.Context
	shared  *clientState
	session *jxaSession
	// Only a local cross-mailbox search copy sets this immutable named-account
	// snapshot. It never marks the shared global account cache as complete.
	readAccountSnapshot *Account
}

func NewClient() *Client {
	return &Client{ctx: context.Background(), shared: &clientState{}}
}

// WithContext returns a client whose subprocesses are cancelled with ctx. It
// shares caches with the receiver.
func (c *Client) WithContext(ctx context.Context) *Client {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Client{ctx: ctx, shared: c.shared, readAccountSnapshot: c.readAccountSnapshot}
}

// Context is the context the client's subprocesses run under.
func (c *Client) Context() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// Done reports whether the client's context has been cancelled.
func (c *Client) Done() error {
	return c.Context().Err()
}

// BodySourceDisk reads a message body from Mail.app's message file and asks
// Mail.app only when there is no readable file. BodySourceMail always asks
// Mail.app to render the body.
const (
	BodySourceDisk = "disk"
	BodySourceMail = "mail"
)

// SetBodySource chooses where message bodies come from. The default is
// BodySourceDisk.
func (c *Client) SetBodySource(source string) error {
	switch source {
	case "", BodySourceDisk:
		c.shared.bodyFromMail = false
	case BodySourceMail:
		c.shared.bodyFromMail = true
	default:
		return fmt.Errorf("body source must be %q or %q", BodySourceDisk, BodySourceMail)
	}
	return nil
}

// SetBodyAlternatives includes independently extracted MIME alternatives in
// message output. Counts and warnings are retained when their texts are omitted.
func (c *Client) SetBodyAlternatives(include bool) { c.shared.bodyAlternatives = include }
