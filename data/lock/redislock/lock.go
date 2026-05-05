// Package redislock provides distributed mutual exclusion using Redis SET NX.
//
// Limitation: this lock does NOT provide fencing tokens. If the lock holder's
// TTL expires while it is still processing (e.g. due to a GC pause or slow
// I/O), a second process can acquire the lock and both may write to shared
// resources concurrently. For critical sections that write to databases, use
// database-level locking (SELECT FOR UPDATE) or implement fencing tokens at
// the application layer. For Postgres-backed work, see
// data/lock/pgadvisorylock (proposed) for session-scoped advisory locks
// that automatically release on connection death.
//
// # Usage
//
// Prefer the [Locker] API:
//
//	lc := redislock.NewLocker(client, redislock.WithTTL(30*time.Second))
//	if err := lc.WithLock(ctx, "order:42", func(ctx context.Context) error {
//	    // critical section
//	    return nil
//	}); err != nil {
//	    if errors.Is(err, lock.ErrLockLost) {
//	        // TTL expired mid-section — caller must reconcile
//	    }
//	    return err
//	}
//
// The legacy stateful [New] / [*Lock.Acquire] form is retained for backward
// compatibility but is deprecated; it offers no protection against the
// re-acquire-without-release footgun. Prefer [NewLocker] for new code.
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

	"github.com/bds421/rho-kit/data/lock"
)

// releaseScript atomically releases the lock only if the caller still owns it.
// Returns 1 on successful DEL, 0 on token mismatch (caller lost the lock).
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

// Option configures a Lock or Locker.
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
// Acquire will retry at the given interval for up to maxAttempts times
// before returning (false, nil).
func WithRetry(interval time.Duration, maxAttempts int) Option {
	return func(o *options) {
		o.retryInterval = interval
		o.maxAttempts = maxAttempts
	}
}

// Locker is a long-lived factory for per-key distributed locks. It implements
// [lock.Locker], so callers depending on the kit-level interface can swap in
// alternative backends (for example pgadvisorylock).
//
// A single Locker is safe for concurrent use; each Acquire produces a fresh
// Lock handle with its own owner token, eliminating the re-acquire footgun
// of the legacy stateful API.
type Locker struct {
	client redis.UniversalClient
	opts   options
}

// NewLocker creates a Locker bound to the given Redis client. The options
// (TTL, retry interval, retry attempts) become the defaults for every
// Acquire call.
func NewLocker(client redis.UniversalClient, opts ...Option) *Locker {
	o := options{ttl: 30 * time.Second}
	for _, fn := range opts {
		fn(&o)
	}
	return &Locker{client: client, opts: o}
}

// Acquire attempts to acquire a lock for `key`. On success, returns a Lock
// handle bound to a fresh owner token; the caller MUST eventually Release the
// returned handle (or rely on TTL expiry). On contention, retries per the
// Locker's configured retry policy before returning (nil, false, nil).
//
// Returns (nil, false, ctx.Err()) if the context is cancelled while waiting
// to retry.
func (lc *Locker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	handle := &Lock{
		client: lc.client,
		key:    key,
		token:  generateToken(),
		opts:   lc.opts,
	}

	ok, err := handle.tryAcquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if ok {
		return handle, true, nil
	}
	if lc.opts.maxAttempts == 0 {
		return nil, false, nil
	}

	for attempt := 1; attempt < lc.opts.maxAttempts; attempt++ {
		t := time.NewTimer(lc.opts.retryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, false, ctx.Err()
		case <-t.C:
		}
		ok, err := handle.tryAcquire(ctx)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return handle, true, nil
		}
	}
	return nil, false, nil
}

// WithLock acquires `key`, runs fn, and releases the lock. The lock is
// released even if fn returns an error or panics. If Release detects the
// lock was lost mid-fn (TTL expired), [lock.ErrLockLost] is joined with
// fn's error so callers can inspect both via errors.Is.
//
// Release uses a fresh background context (not the caller's ctx) so the lock
// is released even if fn exhausted a deadline or was cancelled.
func (lc *Locker) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (retErr error) {
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		return fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !ok {
		return fmt.Errorf("lock: could not acquire lock %q", key)
	}

	defer releaseAndJoin(l, key, &retErr)
	return fn(ctx)
}

// releaseAndJoin runs Release and modifies retErr to surface ErrLockLost.
// It is invoked via defer so the lock is released on panic as well as on
// normal return / error return. Backend Release errors that aren't
// ErrLockLost are logged rather than returned, because fn's error (if any)
// is more actionable for the caller and the lock will TTL out regardless.
func releaseAndJoin(l lock.Lock, key string, retErr *error) {
	relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	relErr := l.Release(relCtx)
	if errors.Is(relErr, lock.ErrLockLost) {
		if *retErr != nil {
			*retErr = errors.Join(*retErr, relErr)
			return
		}
		*retErr = relErr
		return
	}
	if relErr != nil {
		slog.Error("lock: failed to release after WithLock",
			"key", key,
			"error", relErr,
		)
	}
}

// Lock provides distributed mutual exclusion using Redis. A Lock is safe for
// use by a single goroutine at a time. Create separate Lock instances (via
// [Locker.Acquire] — preferred — or the legacy [New]) for concurrent
// acquisition attempts.
type Lock struct {
	client redis.UniversalClient
	key    string
	token  string
	opts   options
}

// New creates a stateful Lock for the given key.
//
// Deprecated: Use [NewLocker] + [Locker.Acquire]. The stateful pattern allows
// callers to call Acquire twice on the same handle, which silently orphans
// the previous Redis lock until TTL expiry. The Locker form returns a fresh
// handle per Acquire and removes the footgun.
func New(client redis.UniversalClient, key string, opts ...Option) *Lock {
	o := options{ttl: 30 * time.Second}
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
// WARNING: Do not call Acquire on a lock that is already held — it returns
// an error rather than silently regenerating the token (which would orphan
// the previous Redis lock until TTL). Always Release first, or use
// [NewLocker] / [Locker.Acquire] for the safer per-call-handle pattern.
//
// Deprecated: Use [Locker.Acquire].
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
//	        if ok, _ := lock.Extend(ctx); !ok { return lock.ErrLockLost }
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

// Release releases the lock. Returns [lock.ErrLockLost] if the lock had
// already expired or been claimed by another process by the time Release ran
// — callers performing critical-section work should treat that as "my work
// may have raced with another holder; reconcile." Returns nil if the lock
// was successfully released or if no lock was ever acquired on this handle.
func (l *Lock) Release(ctx context.Context) error {
	if l.token == "" {
		// Nothing to release — Acquire failed or was never called.
		return nil
	}
	result, err := releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Int64()
	l.token = "" // allow re-acquire either way
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("lock: release failed: %w", err)
	}
	if result == 0 {
		return lock.ErrLockLost
	}
	return nil
}

// WithLock acquires the lock, runs fn, and releases the lock. The lock is
// released even if fn returns an error or panics. If acquisition fails,
// WithLock returns an error without calling fn.
//
// If Release detects the lock was lost mid-fn, [lock.ErrLockLost] is joined
// with fn's error so callers can inspect both via errors.Is.
//
// Release uses a fresh background context (not the caller's ctx) to ensure
// the lock is released even if fn exhausted a deadline or was cancelled.
//
// WARNING: This lock does NOT provide fencing tokens. If fn takes longer
// than the lock TTL, another process can acquire the lock concurrently.
// For critical database writes, use SELECT FOR UPDATE or application-layer
// fencing. See package doc for details.
//
// Deprecated: Use [Locker.WithLock].
func (l *Lock) WithLock(ctx context.Context, fn func(ctx context.Context) error) (retErr error) {
	acquired, err := l.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !acquired {
		return fmt.Errorf("lock: could not acquire lock %q", l.key)
	}

	defer releaseAndJoin(l, l.key, &retErr)
	return fn(ctx)
}

// WithLockValue acquires the lock, runs fn, and releases the lock, returning
// the value produced by fn. This is a generic alternative to [Lock.WithLock]
// that avoids closure variables for callers that need a return value from
// the critical section.
//
// Deprecated: Use [LockerWithValue] with [NewLocker].
func WithLockValue[T any](ctx context.Context, l *Lock, fn func(context.Context) (T, error)) (value T, retErr error) {
	acquired, err := l.Acquire(ctx)
	if err != nil {
		return value, fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !acquired {
		return value, fmt.Errorf("lock: could not acquire lock %q", l.key)
	}

	defer releaseAndJoin(l, l.key, &retErr)
	return fn(ctx)
}

// LockerWithValue acquires the lock for `key`, runs fn, releases the lock,
// and returns the value fn produced. Surfaces [lock.ErrLockLost] joined with
// fn's error if the TTL expired during fn.
func LockerWithValue[T any](ctx context.Context, lc *Locker, key string, fn func(context.Context) (T, error)) (value T, retErr error) {
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		return value, fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !ok {
		return value, fmt.Errorf("lock: could not acquire lock %q", key)
	}

	defer releaseAndJoin(l, key, &retErr)
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
