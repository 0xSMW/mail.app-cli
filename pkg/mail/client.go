package mail

import (
	"sync"
)

type Client struct {
	accountsMu               sync.Mutex
	accounts                 []Account
	accountsLoaded           bool
	indexFallbackWarningOnce sync.Once
	contentWarningOnce       sync.Once
	recentCleanupWarningOnce sync.Once
}

func NewClient() *Client {
	return &Client{}
}
