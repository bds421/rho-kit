// Package redsyncutil holds shared redsync helpers used by both the
// single-instance redislock package and the quorum redlock subpackage.
// Keeping the contention/lost-lock classification, backoff math, key
// validation, and handle release/extend paths in one place prevents the
// two lockers from silently drifting (review-14).
package redsyncutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/go-redsync/redsync/v4"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
)

// MaxLockKeyLen mirrors [lock.MaxKeyLen].
const MaxLockKeyLen = lock.MaxKeyLen

// ValidateLockKey delegates to [lock.ValidateKey] and prefixes the error
// with label ("redislock" or "redlock") so log lines keep their package
// identity. The core contract lives in package lock so backends cannot
// silently diverge (review-12).
func ValidateLockKey(label, key string) error {
	if err := lock.ValidateKey(key); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// TryCount maps the kit's maxAttempts option (0 = single shot) to
// redsync's tries semantics (>=1, where 1 means "no retry").
func TryCount(maxAttempts int) int {
	if maxAttempts <= 0 {
		return 1
	}
	return maxAttempts
}

// JitteredBackoff returns a redsync DelayFunc that draws a delay
// uniformly from [base*0.75, base*1.25]. base==0 falls back to
// redsync's default delay range (50–250 ms).
func JitteredBackoff(base time.Duration) redsync.DelayFunc {
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

// IsContentionError reports whether err signals "lock is held by
// another process" rather than a backend failure.
func IsContentionError(err error) bool {
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

// IsLockLostError reports whether err carries redsync's "this token
// no longer owns the key" signal.
func IsLockLostError(err error) bool {
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

// DetachedReleaseContext returns a timeout-bounded context that preserves
// parent values but is not cancelled when parent is.
func DetachedReleaseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// Handle owns a redsync mutex after a successful Acquire. Both redislock
// and redlock return this type (wrapped or direct) as lock.Lock.
type Handle struct {
	Mutex *redsync.Mutex
	// Released is set atomically so a heartbeat goroutine calling
	// Extend concurrent with the owner's Release does not race.
	Released atomic.Bool
	// ErrLabel is the package prefix in wrapped errors ("lock" / "redlock").
	ErrLabel string
}

// NewHandle returns a Handle for a successfully-acquired mutex.
func NewHandle(mutex *redsync.Mutex, errLabel string) *Handle {
	return &Handle{Mutex: mutex, ErrLabel: errLabel}
}

// DoRelease unlocks the mutex. Terminal lost-lock conditions set
// Released and return lock.ErrLockLost; transport errors leave the
// handle retryable.
func (h *Handle) DoRelease(ctx context.Context) error {
	if h.Released.Load() {
		return lock.ErrLockLost
	}
	ok, err := h.Mutex.UnlockContext(ctx)
	if ok {
		h.Released.Store(true)
		return nil
	}
	if err == nil || IsLockLostError(err) {
		h.Released.Store(true)
		return lock.ErrLockLost
	}
	return redact.WrapError(h.ErrLabel+": release failed", err)
}

// DoExtend extends the mutex TTL when still owned.
func (h *Handle) DoExtend(ctx context.Context) (bool, error) {
	ok, err := h.Mutex.ExtendContext(ctx)
	if ok {
		return true, nil
	}
	if err == nil || IsLockLostError(err) {
		return false, nil
	}
	return false, redact.WrapError(h.ErrLabel+": extend failed", err)
}

// ReleaseAndJoin runs Release and modifies retErr to surface
// ErrLockLost. Backend errors that aren't ErrLockLost are logged.
// logMsg is the slog message (e.g. "lock: failed to release after WithLock").
func ReleaseAndJoin(ctx context.Context, l lock.Lock, retErr *error, logger *slog.Logger, logMsg string) {
	relCtx, cancel := DetachedReleaseContext(ctx, 5*time.Second)
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
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error(logMsg, redact.Error(relErr))
	}
}
