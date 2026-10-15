package cmd

import (
	"time"

	"github.com/0xSMW/mail.app-cli/v3/pkg/cache"
)

// loadCached keeps cache failures supplemental to the live read. skipCache
// disables entry reads and writes while retaining cache initialization, as
// content-bearing message lists have always done.
func loadCached[T any](key string, noCache, forceRefresh, skipCache bool, ttl time.Duration, load func() (T, error)) (T, string, error) {
	var c *cache.Cache
	if !noCache {
		c, _ = cache.New()
	}
	if c != nil {
		c.SetTTL(ttl)
	}
	if c != nil && !forceRefresh && !skipCache {
		var value T
		if found, err := c.Get(key, &value); err == nil && found {
			return value, "cache", nil
		}
	}
	value, err := load()
	if err == nil && c != nil && !skipCache {
		_ = c.Set(key, value)
	}
	return value, "live", err
}
