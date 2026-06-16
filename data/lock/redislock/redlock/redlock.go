package redlock

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	goredislib "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
)

// Option configures a [QuorumLocker]. The shape mirrors
// [redislock.Option] so callers swapping a single-instance locker for
// a quorum locker see the same tuning surface.
type Option func(*options)

type options struct {
	ttl           time.Duration
	retryInterval time.Duration
	maxAttempts   int
	maxWait       time.Duration
}

// WithTTL sets the lock expiration duration. Defaults to 30 seconds.
// Choose a TTL comfortably longer than the worst-case critical
// section AND the worst-case Acquire latency across instances —
// Redlock is only safe when TTL >> RTT.
func WithTTL(d time.Duration) Option {
	if d <= 0 {
		panic("redlock: WithTTL requires a positive duration")
	}
	return func(o *options) { o.ttl = d }
}

// WithRetry configures contention-retry polling with the same shape
// as [redislock.WithRetry]: maxAttempts of jittered ±25% delay around
// interval.
func WithRetry(interval time.Duration, maxAttempts int) Option {
	if interval <= 0 {
		panic("redlock: WithRetry requires a positive interval")
	}
	if maxAttempts < 0 {
		panic("redlock: WithRetry requires maxAttempts >= 0")
	}
	return func(o *options) {
		o.retryInterval = interval
		o.maxAttempts = maxAttempts
	}
}

// WithMaxWait caps the total wall-clock retry duration for Acquire.
// Useful when no ctx deadline is configured; omit to disable the cap.
func WithMaxWait(d time.Duration) Option {
	if d <= 0 {
		panic("redlock: WithMaxWait requires a positive duration")
	}
	return func(o *options) { o.maxWait = d }
}

// MaxLockKeyLen mirrors [redislock.MaxLockKeyLen] — the same key
// hygiene applies whether the lock is single-instance or quorum.
const MaxLockKeyLen = 1024

func validateLockKey(key string) error {
	if key == "" {
		return errors.New("redlock: lock key must not be empty")
	}
	if len(key) > MaxLockKeyLen {
		return errors.New("redlock: lock key exceeds maximum length")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x20 || c == 0x7f {
			return errors.New("redlock: lock key contains control bytes")
		}
	}
	return nil
}

// QuorumLocker acquires distributed locks via the Redlock algorithm
// against a quorum of independent Redis instances. It implements
// [lock.Locker] so callers depending on the kit-level interface can
// swap between single-instance ([redislock]) and quorum locking
// without code changes.
//
// Safe for concurrent use. Each Acquire produces a fresh Lock handle
// with its own owner token.
type QuorumLocker struct {
	pools []redis.Pool
	rs    *redsync.Redsync
	opts  options
}

// NewQuorumLocker constructs a QuorumLocker over the supplied Redis
// clients. Each client should point at an INDEPENDENT Redis instance
// — pointing two clients at the same instance defeats the algorithm
// (and any HA promise it provides). Panics if fewer than three
// clients are supplied: Redlock with N<3 cannot tolerate any single
// node loss, so it provides no availability win over a single-pool
// locker.
//
// An ODD count is recommended (typically 3 or 5) so a clean
// majority always exists.
func NewQuorumLocker(clients []goredislib.UniversalClient, opts ...Option) *QuorumLocker {
	if len(clients) < 3 {
		panic("redlock: NewQuorumLocker requires at least 3 independent Redis clients (use redislock.NewLocker for a single instance)")
	}
	pools := make([]redis.Pool, 0, len(clients))
	for i, c := range clients {
		if c == nil {
			panic("redlock: NewQuorumLocker requires non-nil clients (index " + strconv.Itoa(i) + ")")
		}
		pools = append(pools, goredis.NewPool(c))
	}
	o := options{ttl: 30 * time.Second}
	for _, fn := range opts {
		if fn == nil {
			panic("redlock: NewQuorumLocker option must not be nil")
		}
		fn(&o)
	}
	return &QuorumLocker{
		pools: pools,
		rs:    redsync.New(pools...),
		opts:  o,
	}
}

// Acquire attempts to acquire the quorum lock for `key`. Mirrors
// [redislock.Locker.Acquire]: returns (Lock, true, nil) on success,
// (nil, false, nil) on retry-exhausted contention, (nil, false, err)
// on backend failure or ctx cancellation.
func (q *QuorumLocker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}

	mutex := q.rs.NewMutex(key,
		redsync.WithExpiry(q.opts.ttl),
		redsync.WithTries(tryCount(q.opts.maxAttempts)),
		redsync.WithRetryDelayFunc(jitteredBackoff(q.opts.retryInterval)),
	)

	lockCtx := ctx
	var cancelMaxWait context.CancelFunc
	if q.opts.maxWait > 0 {
		lockCtx, cancelMaxWait = context.WithTimeout(ctx, q.opts.maxWait)
		defer cancelMaxWait()
	}

	err := mutex.LockContext(lockCtx)
	if err == nil {
		return &handle{mutex: mutex}, true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if isContentionError(err) {
		return nil, false, nil
	}
	// The internal maxWait timeout (lockCtx) can fire *during* a redsync
	// command on the final try, in which case redsync returns the raw node
	// errors (RedisErrors wrapping context.DeadlineExceeded) rather than a
	// contention sentinel. The caller's ctx is still alive here, so this is
	// an internal maxWait expiry: honour the documented (nil, false, nil)
	// "contention exhausted" contract instead of leaking it as a backend error.
	if lockCtx.Err() != nil {
		return nil, false, nil
	}
	return nil, false, redact.WrapError("redlock: acquire failed", err)
}

// WithLock acquires `key`, runs fn, and releases the lock. Mirrors
// [redislock.Locker.WithLock]: the release uses a detached caller
// context so the lock is freed even if fn cancelled the parent ctx,
// and [lock.ErrLockLost] from the release path is joined with fn's
// error so callers see both.
func (q *QuorumLocker) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (retErr error) {
	l, ok, err := q.Acquire(ctx, key)
	if err != nil {
		return redact.WrapError("redlock: acquire failed", err)
	}
	if !ok {
		return errors.New("redlock: could not acquire lock")
	}
	defer releaseAndJoin(ctx, l, &retErr)
	return fn(ctx)
}

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
		slog.Error("redlock: failed to release after WithLock",
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

type handle struct {
	mutex    *redsync.Mutex
	released bool
}

func (l *handle) Release(ctx context.Context) error {
	if l.released {
		// A second Release reports ErrLockLost rather than nil so the
		// quorum locker matches redislock/pgadvisory and the kit's
		// lock.Lock contract (locktest testDoubleReleaseLost). Callers
		// wanting idempotent cleanup catch it via
		// errors.Is(err, lock.ErrLockLost).
		return lock.ErrLockLost
	}
	ok, err := l.mutex.UnlockContext(ctx)
	if ok {
		l.released = true
		return nil
	}
	l.released = true
	if err == nil || isLockLostError(err) {
		return lock.ErrLockLost
	}
	return redact.WrapError("redlock: release failed", err)
}

func (l *handle) Extend(ctx context.Context) (bool, error) {
	ok, err := l.mutex.ExtendContext(ctx)
	if ok {
		return true, nil
	}
	if err == nil || isLockLostError(err) {
		return false, nil
	}
	return false, redact.WrapError("redlock: extend failed", err)
}

func tryCount(maxAttempts int) int {
	if maxAttempts <= 0 {
		return 1
	}
	return maxAttempts
}

func jitteredBackoff(base time.Duration) redsync.DelayFunc {
	if base <= 0 {
		return func(int) time.Duration {
			const lo = 50 * time.Millisecond
			const hi = 250 * time.Millisecond
			return lo + time.Duration(rand.Int64N(int64(hi-lo)))
		}
	}
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
