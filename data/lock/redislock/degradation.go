package redislock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bds421/rho-kit/infra/redis"
)

// ErrUnavailable is returned when the lock cannot be acquired because Redis
// is unavailable and the degradation policy rejects the operation.
var ErrUnavailable = errors.New("lock: redis unavailable")

// DegradedLock wraps a [Lock] with a health check that consults the
// [redis.Connection] before attempting Redis operations. When Redis is
// unhealthy, the lock's behavior depends on the configured
// [redis.DegradationPolicy]:
//
//   - [redis.FailFastPolicy]: returns [ErrUnavailable] immediately.
//   - [redis.PassthroughPolicy] or custom: delegates to the underlying lock,
//     which will likely fail with a Redis error.
//
// DegradedLock satisfies the same usage patterns as [Lock] but is NOT safe
// for concurrent use from multiple goroutines (same as [Lock]).
type DegradedLock struct {
	inner  *Lock
	conn   *redis.Connection
	policy redis.DegradationPolicy
}

// NewDegraded creates a lock with degradation support. When Redis is unhealthy,
// the policy determines behavior. All other parameters are forwarded to [New].
//
// Panics if conn or policy is nil.
func NewDegraded(
	conn *redis.Connection,
	key string,
	policy redis.DegradationPolicy,
	opts ...Option,
) *DegradedLock {
	if conn == nil {
		panic("redislock: connection must not be nil")
	}
	if policy == nil {
		panic("redislock: degradation policy must not be nil")
	}
	return &DegradedLock{
		inner:  New(conn.Client(), key, opts...),
		conn:   conn,
		policy: policy,
	}
}

// Acquire attempts to acquire the lock. If Redis is unhealthy and the
// degradation policy returns an error, that error is returned wrapped
// with [ErrUnavailable]. Otherwise, the operation is delegated to the
// underlying [Lock].
func (dl *DegradedLock) Acquire(ctx context.Context) (bool, error) {
	if err := dl.checkHealth(ctx); err != nil {
		return false, err
	}
	return dl.inner.Acquire(ctx)
}

// Release releases the lock. If Redis is unhealthy and the degradation
// policy returns an error, that error is returned. Otherwise, the operation
// is delegated to the underlying [Lock].
func (dl *DegradedLock) Release(ctx context.Context) error {
	if err := dl.checkHealth(ctx); err != nil {
		return err
	}
	return dl.inner.Release(ctx)
}

// Extend extends the lock's TTL. If Redis is unhealthy and the degradation
// policy returns an error, that error is returned. Otherwise, the operation
// is delegated to the underlying [Lock].
func (dl *DegradedLock) Extend(ctx context.Context) (bool, error) {
	if err := dl.checkHealth(ctx); err != nil {
		return false, err
	}
	return dl.inner.Extend(ctx)
}

// WithLock acquires the lock, runs fn, and releases the lock.
// If Redis is unhealthy and the degradation policy rejects the operation,
// fn is not called and an error is returned.
func (dl *DegradedLock) WithLock(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := dl.checkHealth(ctx); err != nil {
		return err
	}
	return dl.inner.WithLock(ctx, fn)
}

// TTL returns the configured lock TTL duration.
func (dl *DegradedLock) TTL() time.Duration {
	return dl.inner.TTL()
}

// checkHealth consults the connection health and applies the degradation
// policy. Returns nil if healthy or if the policy allows passthrough.
func (dl *DegradedLock) checkHealth(ctx context.Context) error {
	if dl.conn.Healthy() {
		return nil
	}
	if err := dl.policy.OnUnavailable(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}
