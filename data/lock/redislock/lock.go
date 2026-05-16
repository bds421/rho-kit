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
package redislock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
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
	maxWait       time.Duration
}

// WithTTL sets the lock expiration duration. Defaults to 30 seconds.
func WithTTL(d time.Duration) Option {
	if d <= 0 {
		panic("redislock: WithTTL requires a positive duration")
	}
	return func(o *options) { o.ttl = d }
}

// WithRetry configures polling when the lock is held by another process.
// Acquire will retry at the given interval for up to maxAttempts times
// before returning (false, nil).
func WithRetry(interval time.Duration, maxAttempts int) Option {
	if interval <= 0 {
		panic("redislock: WithRetry requires a positive interval")
	}
	if maxAttempts < 0 {
		panic("redislock: WithRetry requires maxAttempts >= 0")
	}
	return func(o *options) {
		o.retryInterval = interval
		o.maxAttempts = maxAttempts
	}
}

// WithMaxWait caps the total wall-clock time Acquire is willing to
// spend retrying. Useful when the caller has no ctx deadline and wants
// to bound retries by elapsed time rather than attempt count alone.
// Omit this option to leave the cap disabled; only ctx-cancellation
// and maxAttempts apply.
func WithMaxWait(d time.Duration) Option {
	if d <= 0 {
		panic("redislock: WithMaxWait requires a positive duration")
	}
	return func(o *options) {
		o.maxWait = d
	}
}

// Locker is a long-lived factory for per-key distributed locks. It implements
// [lock.Locker], so callers depending on the kit-level interface can swap in
// alternative backends (for example pgadvisorylock).
//
// A single Locker is safe for concurrent use; each Acquire produces a fresh
// Lock handle with its own owner token.
type Locker struct {
	client redis.UniversalClient
	opts   options
}

var tokenRandReader io.Reader = rand.Reader

// MaxLockKeyLen caps the byte length of a lock key passed to
// [Locker.Acquire] / [Locker.WithLock]. Beyond this length, Redis
// keys become awkward to inspect and large keys hurt cluster
// performance. The cap matches the kit's tenant-scoped key cap.
const MaxLockKeyLen = 1024

// validateLockKey enforces the kit's lock-key shape: non-empty, no
// control characters or whitespace, length within MaxLockKeyLen.
// Wave 71 added this guard to close a hostile-review finding that
// redislock accepted arbitrary bytes (including newlines and NUL)
// as lock keys, which can corrupt logs and Redis ACL evaluation.
func validateLockKey(key string) error {
	if key == "" {
		return errors.New("redislock: lock key must not be empty")
	}
	if len(key) > MaxLockKeyLen {
		return errors.New("redislock: lock key exceeds maximum length")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x20 || c == 0x7f {
			return errors.New("redislock: lock key contains control bytes")
		}
	}
	return nil
}

// NewLocker creates a Locker bound to the given Redis client. The options
// (TTL, retry interval, retry attempts) become the defaults for every
// Acquire call. Panics if client is nil — a miswired locker would otherwise
// dereference nil on the first Acquire.
func NewLocker(client redis.UniversalClient, opts ...Option) *Locker {
	if client == nil {
		panic("redislock: NewLocker requires a non-nil Redis client")
	}
	o := options{ttl: 30 * time.Second}
	for _, fn := range opts {
		if fn == nil {
			panic("redislock: NewLocker option must not be nil")
		}
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
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}
	token, err := generateToken()
	if err != nil {
		return nil, false, err
	}
	h := &handle{
		client: lc.client,
		key:    key,
		token:  token,
		opts:   lc.opts,
	}

	start := time.Now()
	ok, err := h.tryAcquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if ok {
		return h, true, nil
	}
	if lc.opts.maxAttempts == 0 {
		return nil, false, nil
	}

	for attempt := 1; attempt < lc.opts.maxAttempts; attempt++ {
		// Bound by total wall-clock so a long retryInterval × maxAttempts
		// can't outlast the caller's effective deadline. If the caller's
		// ctx has its own deadline that fires first, the select below
		// catches it; this max-elapsed cap protects callers using
		// context.Background().
		if lc.opts.maxWait > 0 && time.Since(start) >= lc.opts.maxWait {
			return nil, false, nil
		}
		t := time.NewTimer(lc.opts.retryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, false, ctx.Err()
		case <-t.C:
		}
		ok, err := h.tryAcquire(ctx)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return h, true, nil
		}
	}
	return nil, false, nil
}

// WithLock acquires `key`, runs fn, and releases the lock. The lock is
// released even if fn returns an error or panics. If Release detects the
// lock was lost mid-fn (TTL expired), [lock.ErrLockLost] is joined with
// fn's error so callers can inspect both via errors.Is.
//
// Release uses a timeout-bounded detached caller context so the lock is
// released even if fn exhausted a deadline or was cancelled, while preserving
// context values used by tracing/logging wrappers.
func (lc *Locker) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (retErr error) {
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		return fmt.Errorf("lock: acquire failed: %w", err)
	}
	if !ok {
		return errors.New("lock: could not acquire lock")
	}

	defer releaseAndJoin(ctx, l, &retErr)
	return fn(ctx)
}

// releaseAndJoin runs Release and modifies retErr to surface ErrLockLost.
// It is invoked via defer so the lock is released on panic as well as on
// normal return / error return. Backend Release errors that aren't
// ErrLockLost are logged rather than returned, because fn's error (if any)
// is more actionable for the caller and the lock will TTL out regardless.
func releaseAndJoin(ctx context.Context, l lock.Lock, retErr *error) {
	relCtx, cancel := detachedReleaseContext(ctx, 5*time.Second)
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
			redact.Error(relErr),
		)
	}
}

func detachedReleaseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// handle is the internal lock-state struct that backs the [lock.Lock]
// returned by [Locker.Acquire]. Each successful Acquire produces a fresh
// handle with a unique token; the previous public Lock type and its
// stateful Acquire/Release/Extend methods were removed in v2 because the
// re-Acquire footgun couldn't be guarded against statically.
type handle struct {
	client redis.UniversalClient
	key    string
	token  string
	opts   options
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
		return value, errors.New("lock: could not acquire lock")
	}

	defer releaseAndJoin(ctx, l, &retErr)
	return fn(ctx)
}

// Release releases the lock. Returns [lock.ErrLockLost] if the lock had
// already expired or been claimed by another process by the time Release
// ran — callers performing critical-section work should treat that as
// "my work may have raced with another holder; reconcile."
//
// The local token is preserved on ambiguous backend errors (network /
// timeout / proxy reset) so a retried Release can still attempt to
// release. The release script is idempotent: a second Release that
// matches the live token will succeed; one that does not match will
// surface ErrLockLost. Wave 66 closed a hostile-review finding where
// the token was cleared before the Run result was inspected, losing
// the ability to retry on ambiguous failures.
func (l *handle) Release(ctx context.Context) error {
	if l.token == "" {
		return nil
	}
	result, err := releaseScript.Run(ctx, l.client, []string{l.key}, l.token).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("lock: release failed: %w", err)
	}
	l.token = ""
	if result == 0 {
		return lock.ErrLockLost
	}
	return nil
}

// Extend resets the lock's TTL to the configured duration, but only if
// the caller still owns the lock (token matches). Returns true if the
// extension succeeded, false if the lock was already released or
// acquired by another process.
func (l *handle) Extend(ctx context.Context) (bool, error) {
	ttlMs := l.opts.ttl.Milliseconds()
	result, err := extendScript.Run(ctx, l.client, []string{l.key}, l.token, ttlMs).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, fmt.Errorf("lock: extend failed: %w", err)
	}
	return result == 1, nil
}

func (l *handle) tryAcquire(ctx context.Context) (bool, error) {
	ok, err := l.client.SetNX(ctx, l.key, l.token, l.opts.ttl).Result()
	if err == nil {
		return ok, nil
	}

	// SETNX returned an error. The SET *might* have landed in Redis even
	// though the client never saw the success reply (TCP RST mid-response,
	// proxy timeout after server-side commit, etc.). Without a probe, the
	// caller would discard our token and treat the slot as unavailable
	// while Redis silently holds the lock until TTL — an "orphan window"
	// that the audit specifically flagged.
	//
	// Best-effort: probe with a short, ctx-scoped GET. If we see OUR token,
	// the SET landed and we own the lock. Otherwise the original error is
	// returned so the caller knows the acquire genuinely failed.
	//
	// 250ms cap (down from 1s in earlier versions) — if the network was
	// flaky enough to break SETNX it's also unlikely to recover in time
	// for a usable probe. Keep the cap tight so a cancelled ctx returns
	// quickly.
	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	current, getErr := l.client.Get(probeCtx, l.key).Result()
	if getErr == nil && current == l.token {
		return true, nil
	}

	return false, fmt.Errorf("lock: acquire failed: %w", err)
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(tokenRandReader, b); err != nil {
		return "", fmt.Errorf("redislock: generate lock token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
