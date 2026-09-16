package proxy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/edwinavalos/onyx/internal/keychain"
)

// SecretSource yields the current value of a named credential. The proxy
// is the only consumer; nothing it serves ever sees the value.
type SecretSource interface {
	Get(ctx context.Context, key string) (string, error)
}

// ErrNoSecret is returned by sources for an unknown key.
var ErrNoSecret = errors.New("secret not found")

// CacheTTL bounds how often a cached source re-reads a secret. Short
// enough that a token refreshed by its owning app (Claude Code rotates
// its OAuth token) is picked up promptly.
const CacheTTL = 30 * time.Second

// Cached wraps a source with a per-key TTL cache.
type Cached struct {
	Source SecretSource
	mu     sync.Mutex
	vals   map[string]cachedVal
}

type cachedVal struct {
	v  string
	at time.Time
}

// Get returns the cached value while fresh, else re-reads it.
func (c *Cached) Get(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = map[string]cachedVal{}
	}
	if e, ok := c.vals[key]; ok && time.Since(e.at) < CacheTTL {
		return e.v, nil
	}
	v, err := c.Source.Get(ctx, key)
	if err != nil {
		return "", err
	}
	c.vals[key] = cachedVal{v: v, at: time.Now()}
	return v, nil
}

// Keychain reads secrets from the macOS Keychain (linked items resolved).
type Keychain struct{}

// Get implements SecretSource.
func (Keychain) Get(ctx context.Context, key string) (string, error) {
	v, err := keychain.Get(ctx, key)
	if errors.Is(err, keychain.ErrNotFound) {
		return "", ErrNoSecret
	}
	return v, err
}
