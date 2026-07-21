package redislock

import (
	"context"
	"errors"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

// ErrUnavailable is returned when the lock cannot be acquired because Redis
// is unavailable and the degradation policy rejects the operation.
var ErrUnavailable = errors.New("lock: redis unavailable")

// DegradedLocker wraps a [Locker] with a health check that consults the
// [redis.Connection] before attempting Redis operations. When Redis is
// unhealthy, the locker's behavior depends on the configured
// [redis.DegradationPolicy]:
//
//   - [redis.FailFastPolicy]: returns [ErrUnavailable] immediately.
//   - [redis.PassthroughPolicy] or custom: delegates to the underlying
//     locker, which will likely fail with a Redis error.
type DegradedLocker struct {
	inner  *Locker
	conn   *redis.Connection
	policy redis.DegradationPolicy
}

// NewDegradedLocker creates a [Locker] with degradation support. When Redis
// is unhealthy, the policy determines behavior.
//
// Panics if conn or policy is nil.
func NewDegradedLocker(
	conn *redis.Connection,
	policy redis.DegradationPolicy,
	opts ...Option,
) *DegradedLocker {
	if conn == nil {
		panic("redislock: NewDegradedLocker connection must not be nil")
	}
	if policy == nil {
		panic("redislock: NewDegradedLocker degradation policy must not be nil")
	}
	return &DegradedLocker{
		inner:  NewLocker(conn.Client(), opts...),
		conn:   conn,
		policy: policy,
	}
}

// Acquire attempts to acquire the lock for the given key. If Redis is
// unhealthy and the degradation policy returns an error, that error is
// wrapped with [ErrUnavailable]. Otherwise, the call is delegated to the
// underlying Locker.
func (dl *DegradedLocker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := dl.checkHealth(ctx); err != nil {
		return nil, false, err
	}
	return dl.inner.Acquire(ctx, key)
}

// WithLock acquires the lock for key, runs fn, and releases the lock.
// If Redis is unhealthy and the degradation policy rejects the operation,
// fn is not called and an error is returned.
func (dl *DegradedLocker) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	if err := dl.checkHealth(ctx); err != nil {
		return err
	}
	return dl.inner.WithLock(ctx, key, fn)
}

// checkHealth consults the connection health and applies the degradation
// policy. Returns nil if healthy or if the policy allows passthrough.
func (dl *DegradedLocker) checkHealth(ctx context.Context) error {
	if dl.conn.Healthy() {
		return nil
	}
	if err := redis.ApplyDegradation(ctx, dl.conn, dl.policy); err != nil {
		return redact.WrapSentinel(ErrUnavailable, err)
	}
	return nil
}
