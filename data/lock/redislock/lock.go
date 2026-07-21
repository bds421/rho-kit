package redislock

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	goredislib "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/lock/redislock/v2/internal/redsyncutil"
	"github.com/bds421/rho-kit/data/v2/lock"
)

// Option configures a Lock or Locker. Shared shape with
// [github.com/bds421/rho-kit/data/lock/redislock/redlock.Option] via
// redsyncutil so TTL/retry/max-wait/prefix cannot drift.
type Option = redsyncutil.Option

// WithTTL sets the lock expiration duration. Defaults to 30 seconds.
func WithTTL(d time.Duration) Option {
	return redsyncutil.WithTTL("redislock", d)
}

// WithRetry configures polling when the lock is held by another process.
// maxAttempts is the TOTAL number of acquisition attempts, not the retry
// count: maxAttempts == 1 makes a single attempt with no retry, and
// maxAttempts == N retries N-1 times at the given interval before
// returning (nil, false, nil). maxAttempts <= 0 also means a single
// attempt.
//
// Wave 126 backed the retry loop with redsync's
// [redsync.WithTries] and [redsync.WithRetryDelayFunc]. The interval
// argument is the base for a ±25% jittered backoff that replaces the
// previous fixed-interval polling, eliminating synchronised retry
// spikes under thundering-herd contention.
func WithRetry(interval time.Duration, maxAttempts int) Option {
	return redsyncutil.WithRetry("redislock", interval, maxAttempts)
}

// WithLogger sets the *slog.Logger the locker uses for the
// post-WithLock release-failure log line. When unset the locker
// falls back to [slog.Default]. Matches the kit's per-package
// [WithLogger] convention.
func WithLogger(l *slog.Logger) Option {
	return redsyncutil.WithLogger("redislock", l)
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
	return redsyncutil.WithMaxWait("redislock", d)
}

// WithKeyPrefix sets the Redis key namespace prepended to every lock key.
// Default is "lock:" so a co-tenant cache/queue using the same logical name
// cannot overwrite a redsync token. Pass "" only for deliberate flat-key
// migration of existing deployments.
func WithKeyPrefix(p string) Option {
	return redsyncutil.WithKeyPrefix("redislock", p)
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
	opts   redsyncutil.Config
}

// MaxLockKeyLen caps the byte length of a lock key passed to
// [Locker.Acquire] / [Locker.WithLock]. Beyond this length, Redis
// keys become awkward to inspect and large keys hurt cluster
// performance. The cap matches the kit's tenant-scoped key cap.
const MaxLockKeyLen = redsyncutil.MaxLockKeyLen

// validateLockKey enforces the kit's lock-key shape: non-empty, no
// control characters or whitespace, length within MaxLockKeyLen.
// Wave 71 added this guard to close a hostile-review finding that
// redislock accepted arbitrary bytes (including newlines and NUL)
// as lock keys, which can corrupt logs and Redis ACL evaluation.
// Wave 126 preserved this guard as the kit's value-add over
// go-redsync/redsync, which does not validate key shape.
func validateLockKey(key string) error {
	return redsyncutil.ValidateLockKey("redislock", key)
}

// NewLocker creates a Locker bound to the given Redis client. The options
// (TTL, retry interval, retry attempts) become the defaults for every
// Acquire call. Panics if client is nil — a miswired locker would otherwise
// dereference nil on the first Acquire.
func NewLocker(client goredislib.UniversalClient, opts ...Option) *Locker {
	if client == nil {
		panic("redislock: NewLocker requires a non-nil Redis client")
	}
	o := redsyncutil.DefaultConfig()
	redsyncutil.Apply("redislock", &o, opts...)
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
	ctx, span := startSpan(ctx, "lock.Acquire")
	defer span.End()
	l, ok, err := lc.doAcquire(ctx, key)
	recordResult(span, err)
	return l, ok, err
}

func (lc *Locker) doAcquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}

	rkey := key
	if lc.opts.Prefix != "" {
		rkey = lc.opts.Prefix + key
	}
	mutex := lc.rs.NewMutex(rkey,
		redsync.WithExpiry(lc.opts.TTL),
		redsync.WithTries(redsyncutil.TryCount(lc.opts.MaxAttempts)),
		redsync.WithRetryDelayFunc(redsyncutil.JitteredBackoff(lc.opts.RetryInterval)),
	)

	lockCtx := ctx
	var cancelMaxWait context.CancelFunc
	if lc.opts.MaxWait > 0 {
		lockCtx, cancelMaxWait = context.WithTimeout(ctx, lc.opts.MaxWait)
		defer cancelMaxWait()
	}

	err := mutex.LockContext(lockCtx)
	if err == nil {
		return &handle{Handle: redsyncutil.NewHandle(mutex, "lock")}, true, nil
	}

	// Surface caller-driven cancellation as-is so existing callers can
	// distinguish ctx errors from contention. Internal maxWait timeouts
	// are absorbed below as "contention exhausted".
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if redsyncutil.IsContentionError(err) {
		return nil, false, nil
	}
	// The internal maxWait timeout (lockCtx) can fire *during* a redsync
	// command on the final try, in which case redsync returns the raw node
	// error wrapping context.DeadlineExceeded rather than a contention
	// sentinel. Only absorb genuine deadline errors as "contention exhausted";
	// hard backend failures (dial refused, auth) must still surface even if
	// maxWait has also elapsed.
	if lockCtx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
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
	ctx, span := startSpan(ctx, "lock.WithLock")
	defer func() { recordResult(span, retErr); span.End() }()
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		// Acquire already wraps backend failures; do not double-prefix.
		return err
	}
	if !ok {
		return lock.ErrNotAcquired
	}

	defer releaseAndJoin(ctx, l, &retErr, lc.opts.Logger)
	return fn(ctx)
}

// releaseAndJoin runs Release and modifies retErr to surface ErrLockLost.
// It is invoked via defer so the lock is released on panic as well as on
// normal return / error return. Backend Release errors that aren't
// ErrLockLost are logged rather than returned, because fn's error (if any)
// is more actionable for the caller and the lock will TTL out regardless.
func releaseAndJoin(ctx context.Context, l lock.Lock, retErr *error, logger *slog.Logger) {
	redsyncutil.ReleaseAndJoin(ctx, l, retErr, logger, "lock: failed to release after WithLock")
}

// handle wraps the shared redsync handle so Release/Extend emit kit spans.
type handle struct {
	*redsyncutil.Handle
}

// LockerWithValue acquires the lock for `key`, runs fn, releases the lock,
// and returns the value fn produced. Surfaces [lock.ErrLockLost] joined with
// fn's error if the TTL expired during fn.
func LockerWithValue[T any](ctx context.Context, lc *Locker, key string, fn func(context.Context) (T, error)) (value T, retErr error) {
	l, ok, err := lc.Acquire(ctx, key)
	if err != nil {
		return value, err
	}
	if !ok {
		return value, lock.ErrNotAcquired
	}

	defer releaseAndJoin(ctx, l, &retErr, lc.opts.Logger)
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
// A second Release on an already-released handle returns
// [lock.ErrLockLost] so callers can errors.Is detect it — same
// contract as pgadvisory.Release and what the kit's
// locktest.RunConformance suite asserts. Callers that want
// idempotent cleanup (e.g. WithLock helpers) catch the error
// via errors.Is(err, lock.ErrLockLost) and treat it as success.
func (l *handle) Release(ctx context.Context) error {
	ctx, span := startSpan(ctx, "lock.Release")
	defer span.End()
	err := l.DoRelease(ctx)
	recordResult(span, err)
	return err
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
	ctx, span := startSpan(ctx, "lock.Extend")
	defer span.End()
	ok, err := l.DoExtend(ctx)
	recordResult(span, err)
	return ok, err
}
