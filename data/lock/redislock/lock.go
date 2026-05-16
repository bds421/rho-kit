package redislock

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	goredislib "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
)

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
// before returning (nil, false, nil).
//
// Wave 126 backed the retry loop with redsync's
// [redsync.WithTries] and [redsync.WithRetryDelayFunc]. The interval
// argument is the base for a ±25% jittered backoff that replaces the
// previous fixed-interval polling, eliminating synchronised retry
// spikes under thundering-herd contention.
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
//
// Wave 126 implements this by wrapping the redsync LockContext call
// with an internal context.WithTimeout. When the internal timeout
// fires before redsync exhausts retries, Acquire returns
// (nil, false, nil) so the contract matches pre-migration behaviour.
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
// alternative backends (for example
// [github.com/bds421/rho-kit/data/lock/pgadvisory/v2]).
//
// A single Locker is safe for concurrent use; each Acquire produces a fresh
// Lock handle with its own owner token.
type Locker struct {
	client goredislib.UniversalClient
	rs     *redsync.Redsync
	opts   options
}

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
// Wave 126 preserved this guard as the kit's value-add over
// go-redsync/redsync, which does not validate key shape.
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
func NewLocker(client goredislib.UniversalClient, opts ...Option) *Locker {
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
	rs := redsync.New(goredis.NewPool(client))
	return &Locker{client: client, rs: rs, opts: o}
}

// Acquire attempts to acquire a lock for `key`. On success, returns a Lock
// handle bound to a fresh owner token; the caller MUST eventually Release the
// returned handle (or rely on TTL expiry). On contention, retries per the
// Locker's configured retry policy before returning (nil, false, nil).
//
// Returns (nil, false, ctx.Err()) if the context is cancelled while waiting
// to retry.
//
// Wave 126: backed by [redsync.Mutex.LockContext]. Contention is detected
// by inspecting the returned error for redsync's "lock taken" sentinels
// ([redsync.ErrFailed], [redsync.ErrTaken], [redsync.ErrNodeTaken]);
// everything else propagates as a backend error.
func (lc *Locker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}

	mutex := lc.rs.NewMutex(key,
		redsync.WithExpiry(lc.opts.ttl),
		redsync.WithTries(tryCount(lc.opts.maxAttempts)),
		redsync.WithRetryDelayFunc(jitteredBackoff(lc.opts.retryInterval)),
	)

	lockCtx := ctx
	var cancelMaxWait context.CancelFunc
	if lc.opts.maxWait > 0 {
		lockCtx, cancelMaxWait = context.WithTimeout(ctx, lc.opts.maxWait)
		defer cancelMaxWait()
	}

	err := mutex.LockContext(lockCtx)
	if err == nil {
		return &handle{mutex: mutex}, true, nil
	}

	// Surface caller-driven cancellation as-is so existing callers can
	// distinguish ctx errors from contention. Internal maxWait timeouts
	// are absorbed below as "contention exhausted".
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if isContentionError(err) {
		return nil, false, nil
	}
	return nil, false, redact.WrapError("lock: acquire failed", err)
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
		return redact.WrapError("lock: acquire failed", err)
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
// handle holding the redsync [*redsync.Mutex] that owns the token and
// pool wiring; the kit's previous bespoke handle (client + token +
// opts) was removed in wave 126 when the redsync migration took over
// the SETNX + token-fenced release/extend Lua scripts.
type handle struct {
	mutex    *redsync.Mutex
	released bool
}

// LockerWithValue acquires the lock for `key`, runs fn, releases the lock,
// and returns the value fn produced. Surfaces [lock.ErrLockLost] joined with
// fn's error if the TTL expired during fn.
func LockerWithValue[T any](ctx context.Context, lc *Locker, key string, fn func(context.Context) (T, error)) (value T, retErr error) {
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		return value, redact.WrapError("lock: acquire failed", err)
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
// Wave 126: backed by [redsync.Mutex.UnlockContext]. Redsync reports the
// lost-lock condition via [redsync.ErrLockAlreadyExpired] (key gone) or a
// [redsync.ErrNodeTaken]/[redsync.ErrTaken] wrapped multierror (key
// still present but token mismatch); both map to [lock.ErrLockLost].
//
// Idempotent: a second Release on the same handle is a no-op.
func (l *handle) Release(ctx context.Context) error {
	if l.released {
		return nil
	}
	ok, err := l.mutex.UnlockContext(ctx)
	if ok {
		l.released = true
		return nil
	}
	// Mark released for the contention/expired path too: a retry against
	// a token we no longer own would never succeed, and the kit's
	// previous behaviour treated ErrLockLost as terminal for the handle.
	l.released = true
	if err == nil || isLockLostError(err) {
		return lock.ErrLockLost
	}
	return redact.WrapError("lock: release failed", err)
}

// Extend resets the lock's TTL to the configured duration, but only if
// the caller still owns the lock (token matches). Returns true if the
// extension succeeded, false if the lock was already released or
// acquired by another process.
//
// Wave 126: backed by [redsync.Mutex.ExtendContext]. The kit preserves
// the (false, nil) contract for "no longer owned" so callers driving a
// heartbeat can branch on ok rather than parse error types.
func (l *handle) Extend(ctx context.Context) (bool, error) {
	ok, err := l.mutex.ExtendContext(ctx)
	if ok {
		return true, nil
	}
	if err == nil || isLockLostError(err) {
		return false, nil
	}
	return false, redact.WrapError("lock: extend failed", err)
}

// tryCount maps the kit's maxAttempts option (0 = single shot) to
// redsync's tries semantics (>=1, where 1 means "no retry"). Defaults
// to 1 when the option is unset.
func tryCount(maxAttempts int) int {
	if maxAttempts <= 0 {
		return 1
	}
	return maxAttempts
}

// jitteredBackoff returns a redsync DelayFunc that draws a delay
// uniformly from [base*0.75, base*1.25]. base==0 falls back to
// redsync's default delay range (50–250 ms).
//
// Wave 126: replaces the kit's previous fixed-interval polling, which
// caused synchronised retry spikes when many waiters contended for
// the same key. Jitter spreads the retries across the interval so
// the thundering-herd resolves probabilistically rather than in
// lockstep bursts.
func jitteredBackoff(base time.Duration) redsync.DelayFunc {
	if base <= 0 {
		// Use redsync's default DelayFunc shape: rand 50–250 ms.
		return func(int) time.Duration {
			const lo = 50 * time.Millisecond
			const hi = 250 * time.Millisecond
			return lo + time.Duration(rand.Int64N(int64(hi-lo)))
		}
	}
	// ±25% jitter around the base.
	const jitterPct = 25
	jitter := int64(base) * jitterPct / 100
	lo := int64(base) - jitter
	span := 2 * jitter
	if span <= 0 {
		return func(int) time.Duration { return base }
	}
	return func(int) time.Duration {
		return time.Duration(lo + rand.Int64N(span))
	}
}

// isContentionError reports whether err signals "lock is held by
// another process" rather than a backend failure. Used by Acquire to
// translate redsync's error vocabulary into the kit's
// (nil, false, nil) contract for retry-exhausted contention.
func isContentionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, redsync.ErrFailed) {
		return true
	}
	var taken *redsync.ErrTaken
	if errors.As(err, &taken) {
		return true
	}
	var nodeTaken *redsync.ErrNodeTaken
	return errors.As(err, &nodeTaken)
}

// isLockLostError reports whether err carries redsync's "this token
// no longer owns the key" signal — either the key expired
// ([redsync.ErrLockAlreadyExpired]) or it was reclaimed by another
// holder ([redsync.ErrNodeTaken] / [redsync.ErrTaken]). Used by
// Release / Extend to fold both shapes into the kit's
// [lock.ErrLockLost] / (false, nil) contracts.
func isLockLostError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, redsync.ErrLockAlreadyExpired) {
		return true
	}
	var taken *redsync.ErrTaken
	if errors.As(err, &taken) {
		return true
	}
	var nodeTaken *redsync.ErrNodeTaken
	return errors.As(err, &nodeTaken)
}
