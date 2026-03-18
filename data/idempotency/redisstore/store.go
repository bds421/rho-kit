// Package idempotencystore provides a Redis-backed implementation of the
// idempotency.Store interface for multi-instance deployments.
//
// Keys are stored as JSON values with a TTL. Processing locks use Redis
// SET NX with a random token and Lua-based conditional delete to ensure
// only the lock owner can release it.
package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/data/idempotency"
)

// unlockScript atomically releases the lock only if the caller still owns it.
//
//	KEYS[1] = lock key
//	ARGV[1] = owner token
var unlockScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// Compile-time interface check.
var _ idempotency.Store = (*RedisStore)(nil)

// Option configures the RedisStore.
type Option func(*RedisStore)

// WithKeyPrefix sets the key prefix for all stored entries.
// Default: "idempotency:".
func WithKeyPrefix(prefix string) Option {
	return func(s *RedisStore) { s.prefix = prefix }
}

// RedisStore implements idempotency.Store using Redis.
// Safe for concurrent use across multiple service instances.
type RedisStore struct {
	client goredis.UniversalClient
	prefix string

	// mu protects tokens. Each goroutine (request) acquires a lock for a
	// unique idempotency key, so contention is low.
	mu     sync.Mutex
	tokens map[string]string // lockKey → token
}

// New creates a RedisStore backed by the given Redis client.
func New(client goredis.UniversalClient, opts ...Option) *RedisStore {
	s := &RedisStore{
		client: client,
		prefix: "idempotency:",
		tokens: make(map[string]string),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *RedisStore) dataKey(key string) string { return s.prefix + key }
func (s *RedisStore) lockKey(key string) string { return s.prefix + key + ":lock" }

// Get returns a cached response for the key, or (nil, nil) if not found.
func (s *RedisStore) Get(ctx context.Context, key string) (*idempotency.CachedResponse, error) {
	data, err := s.client.Get(ctx, s.dataKey(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("idempotencystore: get %q: %w", key, err)
	}
	var resp idempotency.CachedResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("idempotencystore: unmarshal %q: %w", key, err)
	}
	return &resp, nil
}

// Set stores a response for the key with the given TTL.
func (s *RedisStore) Set(ctx context.Context, key string, resp idempotency.CachedResponse, ttl time.Duration) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("idempotencystore: marshal: %w", err)
	}
	if err := s.client.Set(ctx, s.dataKey(key), data, ttl).Err(); err != nil {
		return fmt.Errorf("idempotencystore: set %q: %w", key, err)
	}
	return nil
}

// TryLock attempts to acquire a processing lock for the key using SET NX
// with a random token. Returns true if the lock was acquired. The token is
// stored internally so that only this caller can release it via Unlock.
func (s *RedisStore) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	token := generateToken()
	lk := s.lockKey(key)
	ok, err := s.client.SetNX(ctx, lk, token, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("idempotencystore: lock %q: %w", key, err)
	}
	if ok {
		s.mu.Lock()
		s.tokens[lk] = token
		s.mu.Unlock()
	}
	return ok, nil
}

// Unlock releases the processing lock for the key, but only if the caller
// still owns it (token matches). This prevents one processor from deleting
// a lock that was acquired by another after TTL expiration.
func (s *RedisStore) Unlock(ctx context.Context, key string) error {
	lk := s.lockKey(key)
	s.mu.Lock()
	token := s.tokens[lk]
	delete(s.tokens, lk)
	s.mu.Unlock()

	if token == "" {
		// No token — lock was never acquired or already released.
		return nil
	}

	_, err := unlockScript.Run(ctx, s.client, []string{lk}, token).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("idempotencystore: unlock %q: %w", key, err)
	}
	return nil
}

func generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("idempotencystore: failed to generate token: " + err.Error())
	}
	return hex.EncodeToString(b)
}
