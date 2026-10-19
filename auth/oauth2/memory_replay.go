package oauth2

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultMaxReplayEntries is the default cap on live entries in
// [MemoryReplayCache].
const DefaultMaxReplayEntries = 10_000

// ErrReplayCacheFull is returned by [MemoryReplayCache.Remember] when the
// live entry cap is reached after sweeping expired entries. Callers fail
// closed.
var ErrReplayCacheFull = errors.New("oauth2: replay cache full")

// MemoryReplayCache is an in-process [ReplayCache] for tests and
// single-process services. Replicas that share sessions must share the
// replay cache as well (auth/oauth2/redis implements it). Live entries are
// capped at [DefaultMaxReplayEntries].
type MemoryReplayCache struct {
	mu         sync.Mutex
	entries    map[string]time.Time
	maxEntries int
}

var _ ReplayCache = (*MemoryReplayCache)(nil)

// NewMemoryReplayCache returns an empty cache.
func NewMemoryReplayCache() *MemoryReplayCache {
	return &MemoryReplayCache{entries: make(map[string]time.Time), maxEntries: DefaultMaxReplayEntries}
}

// Remember implements [ReplayCache].
func (m *MemoryReplayCache) Remember(_ context.Context, key string, ttl time.Duration) (bool, error) {
	if key == "" || ttl <= 0 {
		return false, errors.New("oauth2: replay cache requires a key and a positive TTL")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if expiresAt, ok := m.entries[key]; ok && now.Before(expiresAt) {
		return false, nil
	}
	if len(m.entries) >= m.maxEntries {
		for k, expiresAt := range m.entries {
			if !now.Before(expiresAt) {
				delete(m.entries, k)
			}
		}
		if len(m.entries) >= m.maxEntries {
			return false, ErrReplayCacheFull
		}
	}
	m.entries[key] = now.Add(ttl)
	return true, nil
}
