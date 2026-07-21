package redlock

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	goredislib "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/lock/redislock/v2/internal/redsyncutil"
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
	logger        *slog.Logger
	prefix        string
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

// WithLogger sets the *slog.Logger the locker uses for the
// post-WithLock release-failure log line. When unset the locker
// falls back to [slog.Default]. Mirrors [redislock.WithLogger] so
// callers swapping a single-instance locker for a quorum locker keep
// the same observability surface.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithKeyPrefix sets the Redis key namespace prepended to every lock key.
// Default is "lock:" so co-tenant cache keys cannot overwrite quorum tokens.
func WithKeyPrefix(p string) Option {
	return func(o *options) { o.prefix = p }
}

// MaxLockKeyLen mirrors [redislock.MaxLockKeyLen] — the same key
// hygiene applies whether the lock is single-instance or quorum.
const MaxLockKeyLen = redsyncutil.MaxLockKeyLen

func validateLockKey(key string) error {
	return redsyncutil.ValidateLockKey("redlock", key)
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
	o := options{ttl: 30 * time.Second,
		prefix: "lock:",
	}
	for _, fn := range opts {
		if fn == nil {
			panic("redlock: NewQuorumLocker option must not be nil")
		}
		fn(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
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
	ctx, span := startSpan(ctx, "lock.Acquire")
	defer span.End()
	l, ok, err := q.doAcquire(ctx, key)
	recordResult(span, err)
	return l, ok, err
}

func (q *QuorumLocker) doAcquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}

	rkey := key
	if q.opts.prefix != "" {
		rkey = q.opts.prefix + key
	}
	mutex := q.rs.NewMutex(rkey,
		redsync.WithExpiry(q.opts.ttl),
		redsync.WithTries(redsyncutil.TryCount(q.opts.maxAttempts)),
		redsync.WithRetryDelayFunc(redsyncutil.JitteredBackoff(q.opts.retryInterval)),
	)

	lockCtx := ctx
	var cancelMaxWait context.CancelFunc
	if q.opts.maxWait > 0 {
		lockCtx, cancelMaxWait = context.WithTimeout(ctx, q.opts.maxWait)
		defer cancelMaxWait()
	}

	err := mutex.LockContext(lockCtx)
	if err == nil {
		return &handle{Handle: redsyncutil.NewHandle(mutex, "redlock")}, true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if redsyncutil.IsContentionError(err) {
		return nil, false, nil
	}
	// The internal maxWait timeout (lockCtx) can fire *during* a redsync
	// command on the final try, in which case redsync returns the raw node
	// errors (RedisErrors wrapping context.DeadlineExceeded) rather than a
	// contention sentinel. The caller's ctx is still alive here, so this is
	// an internal maxWait expiry: honour the documented (nil, false, nil)
	// "contention exhausted" contract instead of leaking it as a backend error.
	if lockCtx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
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
	ctx, span := startSpan(ctx, "lock.WithLock")
	defer func() { recordResult(span, retErr); span.End() }()
	l, ok, err := q.Acquire(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return lock.ErrNotAcquired
	}
	defer releaseAndJoin(ctx, l, &retErr, q.opts.logger)
	return fn(ctx)
}

func releaseAndJoin(ctx context.Context, l lock.Lock, retErr *error, logger *slog.Logger) {
	redsyncutil.ReleaseAndJoin(ctx, l, retErr, logger, "redlock: failed to release after WithLock")
}

// handle wraps the shared redsync handle so Release/Extend emit kit spans.
type handle struct {
	*redsyncutil.Handle
}

func (l *handle) Release(ctx context.Context) error {
	ctx, span := startSpan(ctx, "lock.Release")
	defer span.End()
	err := l.DoRelease(ctx)
	recordResult(span, err)
	return err
}

func (l *handle) Extend(ctx context.Context) (bool, error) {
	ctx, span := startSpan(ctx, "lock.Extend")
	defer span.End()
	ok, err := l.DoExtend(ctx)
	recordResult(span, err)
	return ok, err
}
