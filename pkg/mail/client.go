package mail

import (
	"context"
	"sync"
)

// clientState is shared by every Client derived from the same NewClient call,
// so a WithContext copy sees the same account cache and warning gates.
type clientState struct {
	accountsMu               sync.Mutex
	accounts                 []Account
	accountsLoaded           bool
	indexFallbackWarningOnce sync.Once
	contentWarningOnce       sync.Once
	recentCleanupWarningOnce sync.Once
	warn                     func(string)
}

// Client talks to Mail.app through osascript and to Mail's Envelope Index
// through sqlite3. Every subprocess it starts is bound to its context, so a
// caller that cancels stops waiting on a queued automation call.
type Client struct {
	ctx    context.Context
	shared *clientState
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
	return &Client{ctx: ctx, shared: c.shared}
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
