package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultTTL is the default cache time-to-live (24 hours)
	DefaultTTL = 24 * time.Hour
	// CacheDir is the cache directory name
	CacheDir = ".cache/mail-app-cli"
)

// Cache manages cached data files
type Cache struct {
	dir string
	ttl time.Duration
}

// New creates a new Cache instance
func New() (*Cache, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, CacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &Cache{
		dir: cacheDir,
		ttl: DefaultTTL,
	}, nil
}

// SetTTL sets the cache time-to-live
func (c *Cache) SetTTL(ttl time.Duration) {
	c.ttl = ttl
}

// Get retrieves data from cache if it exists and is not stale
func (c *Cache) Get(key string, v any) (bool, error) {
	path := filepath.Join(c.dir, key+".json")

	// Check if file exists and is not stale
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to stat cache file: %w", err)
	}

	// Check if cache is stale
	if time.Since(info.ModTime()) > c.ttl {
		return false, nil
	}

	// Read and unmarshal the cached data
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("failed to read cache file: %w", err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		// If unmarshal fails, delete the corrupted cache file
		os.Remove(path)
		return false, fmt.Errorf("failed to unmarshal cache data: %w", err)
	}

	return true, nil
}

// Set stores data in the cache
func (c *Cache) Set(key string, v any) error {
	path := filepath.Join(c.dir, key+".json")

	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Delete removes a cached item
func (c *Cache) Delete(key string) error {
	path := filepath.Join(c.dir, key+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete cache file: %w", err)
	}
	return nil
}

// Clear removes all cached items
func (c *Cache) Clear() error {
	files, err := filepath.Glob(filepath.Join(c.dir, "*.json"))
	if err != nil {
		return fmt.Errorf("failed to list cache files: %w", err)
	}

	for _, file := range files {
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("failed to delete cache file %s: %w", file, err)
		}
	}

	return nil
}
