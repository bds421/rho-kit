// Package lock provides distributed mutual exclusion using Redis SET NX.
//
// Limitation: this lock does NOT provide fencing tokens. If the lock holder's
// TTL expires while it is still processing (e.g., due to a GC pause or slow I/O),
// a second process can acquire the lock and both may write to shared resources
// concurrently. For critical sections that write to databases, use database-level
// locking (SELECT FOR UPDATE) or implement fencing tokens at the application layer.
package redislock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// releaseScript atomically releases the lock only if the caller still owns it.
// This prevents releasing a lock that was acquired by another process after TTL
// expiration.
//
//	KEYS[1] = lock key
//	ARGV[1] = owner token
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// extendScript atomically extends the TTL only if the caller still owns the lock.
// Returns 1 if extended, 0 if the lock is no longer owned.
//
//	KEYS[1] = lock key
//	ARGV[1] = owner token
//	ARGV[2] = new TTL in milliseconds
var extendScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

// Option configures a Lock.
type Option func(*options)

type options struct {
	ttl           time.Duration
	retryInterval time.Duration
	maxAttempts   int
}

// WithTTL sets the lock expiration duration. Defaults to 30 seconds.
func WithTTL(d time.Duration) Option {
	return func(o *options) { o.ttl = d }
}

// WithRetry configures polling when the lock is held by another process.
// The lock will be retried at the given interval for up to maxAttempts times
// before returning (false, nil).
func WithRetry(interval time.Duration, maxAttempts int) Option {
	return func(o *options) {
		o.retryInterval = interval
		o.maxAttempts = maxAttempts
	}
}

// Lock provides distributed mutual exclusion using Redis.
// A Lock is safe for use by a single goroutine at a time. Create separate
// Lock instances for concurrent acquisition attempts.
//
// The token field is NOT protected by a mutex. Concurrent Acquire/Release
// calls on the same Lock instance will race on the token field. This is by
// design: distributed locks represent a single ownership slot, and concurrent
// access from the same process indicates a logic error. Use separate Lock
// instances (via New) for concurrent goroutines.
type Lock struct {
	client redis.UniversalClient
	key    string
	token  string // regenerated per Acquire to prevent stale-state issues
	opts   options
}

// New creates a new distributed lock for the given key. Each Lock instance
// generates a unique token so that only the owner can release it.
func New(client redis.UniversalClient, key string, opts ...Option) *Lock {
	o := options{
		ttl: 30 * time.Second,
	}
	for _, fn := range opts {
		fn(&o)
	}

	return &Lock{
		client: client,
		key:    key,
		opts:   o,
	}
}

// Acquire attempts to acquire the lock. It returns (true, nil) on success,
// (false, nil) if the lock is held by another process, and (false, err) on
// Redis errors. When retry is configured, Acquire polls at the configured
// interval for up to maxAttempts before giving up.
//
// Each call generates a fresh random token to prevent stale-state issues
// if a previous Release failed silently.
//
// WARNING: Do not call Acquire on a lock that is already held — it
// regenerates the token, which makes subsequent Extend/Release calls fail
// silently (token mismatch). Always Release first. Returns an error if
// the lock already holds a token (call Release first).
func (l *Lock) Acquire(ctx context.Context) (bool, error) {
	if l.token != "" {
		return false, fmt.Errorf("lock: already acquired (key %q) — call Release before re-acquiring", l.key)
	}
	l.token = generateToken()

	acquired, err := l.tryAcquire(ctx)
	if err != nil {
		l.token = ""
		return false, err
	}
	if acquired || l.opts.maxAttempts == 0 {
		if !acquired {
			l.token = ""
		}
		return acquired, nil
	}

	for attempt := 1; attempt < l.opts.maxAttempts; attempt++ {
		t := time.NewTimer(l.opts.retryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			l.token = ""
			return false, ctx.Err()
		case <-t.C:
		}

		acquired, err = l.tryAcquire(ctx)
		if err != nil {
			l.token = ""
			return false, err
		}
		if acquired {
			return true, nil
		}
	}

	l.token = ""
	return false, nil
}

// Extend resets the lock's TTL to the configured duration, but only if the
// caller still owns the lock (token matches). Returns true if the extension
// succeeded, false if the lock was already released or acquired by another
// process. Use this to prevent TTL expiry during long-running operations.
//
// A typical pattern for long operations:
//
//	ticker := time.NewTicker(lock.TTL() / 3)
//	defer ticker.Stop()
//	for {
//	    select {
//	    case <-ticker.C:
//	        if ok, _ := lock.Extend(ctx); !ok { return ErrLockLost }
//	    case <-done:
//	        return nil
//	    }
//	}
func (l *Lock) Extend(ctx context.Context) (bool, error) {
	ttlMs := l.opts.ttl.Milliseconds()
	result, err := extendScript.Run(ctx, l.client, []string{l.key}, l.token, ttlMs).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, fmt.Errorf("lock: extend failed: %w", err)
	}
	return result == 1, nil
}

// TTL returns the configured lock TTL duration.
func (l *Lock) TTL() time.Duration {
	return l.opts.ttl
}

// Release releases the lock. Only the owner (matching token) can release it.
// Returns an error if the release fails due to a Redis error. Releasing a lock
// that is not held (or held by another owner) is not an error.
func (l *Lock) Release(ctx context.Context) error {
	_, err := releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("lock: release failed: %w", err)
	}
	l.token = "" // allow re-acquire
	return nil
}

// WithLock acquires the lock, runs fn, and releases the lock. The lock is
// released even if fn returns an error or panics. If acquisition fails,
// WithLock returns an error without calling fn.
//
// Release uses a fresh background context (not the caller's ctx) to ensure
// the lock is released even if fn exhausted a deadline or was cancelled.
//
// WARNING: This lock does NOT provide fencing tokens. If fn takes longer
// than the lock TTL, another process can acquire the lock concurrently.
// For critical database writes, use SELECT FOR UPDATE or application-layer
// fencing. See package doc for details.
func (l *Lock) WithLock(ctx context.Context, fn func(ctx context.Context) error) error {
	acquired, err := l.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !acquired {
		return fmt.Errorf("lock: could not acquire lock %q", l.key)
	}

	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := l.Release(releaseCtx); releaseErr != nil {
			// Log rather than return: the caller's fn error takes priority,
			// and the lock will expire via TTL regardless.
			slog.Error("lock: failed to release after WithLock",
				"key", l.key,
				"error", releaseErr,
			)
		}
	}()

	return fn(ctx)
}

// WithLockValue acquires the lock, runs fn, and releases the lock, returning
// the value produced by fn. This is a generic alternative to [Lock.WithLock]
// that avoids closure variables for callers that need a return value from
// the critical section.
func WithLockValue[T any](ctx context.Context, l *Lock, fn func(context.Context) (T, error)) (T, error) {
	acquired, err := l.Acquire(ctx)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !acquired {
		var zero T
		return zero, fmt.Errorf("lock: could not acquire lock %q", l.key)
	}

	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := l.Release(releaseCtx); releaseErr != nil {
			slog.Error("lock: failed to release after WithLockValue",
				"key", l.key,
				"error", releaseErr,
			)
		}
	}()

	return fn(ctx)
}

func (l *Lock) tryAcquire(ctx context.Context) (bool, error) {
	ok, err := l.client.SetNX(ctx, l.key, l.token, l.opts.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("lock: acquire failed: %w", err)
	}
	return ok, nil
}

func generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("lock: failed to generate token: " + err.Error())
	}
	return hex.EncodeToString(b)
}
