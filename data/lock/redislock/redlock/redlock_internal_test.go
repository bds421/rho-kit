package redlock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/data/lock/redislock/v2/internal/redsyncutil"
	"github.com/bds421/rho-kit/data/v2/lock"
)

// fakeLock is a minimal lock.Lock whose Release returns a configurable
// error. It lets the WithLogger / releaseAndJoin contract be tested
// deterministically without standing up a redsync quorum (whose
// backend-error path requires closing every miniredis instance and
// waiting out connection-pool dial retries).
type fakeLock struct {
	relErr error
}

func (f *fakeLock) Release(context.Context) error { return f.relErr }

func (f *fakeLock) Extend(context.Context) (bool, error) { return false, nil }

var _ lock.Lock = (*fakeLock)(nil)

// TestReleaseAndJoin_BackendErrorGoesToConfiguredLogger verifies that a
// non-ErrLockLost Release failure during WithLock is logged through the
// WithLogger-configured *slog.Logger (mirroring redislock's WithLogger),
// not swallowed and not joined into the returned error.
func TestReleaseAndJoin_BackendErrorGoesToConfiguredLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var retErr error
	releaseAndJoin(context.Background(), &fakeLock{relErr: errors.New("boom")}, &retErr, logger)

	if retErr != nil {
		t.Fatalf("backend release error must not be returned to the caller, got %v", retErr)
	}
	out := buf.String()
	if !strings.Contains(out, "redlock: failed to release after WithLock") {
		t.Fatalf("expected release-failure line in configured logger, got %q", out)
	}
}

// TestReleaseAndJoin_LockLostIsJoinedNotLogged verifies that an
// ErrLockLost from Release is surfaced to the caller via retErr (joined
// when fn already errored) rather than emitted to the logger.
func TestReleaseAndJoin_LockLostIsJoinedNotLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// No prior fn error: retErr becomes ErrLockLost.
	var retErr error
	releaseAndJoin(context.Background(), &fakeLock{relErr: lock.ErrLockLost}, &retErr, logger)
	if !errors.Is(retErr, lock.ErrLockLost) {
		t.Fatalf("ErrLockLost must be surfaced via retErr, got %v", retErr)
	}

	// Prior fn error present: both must be inspectable via errors.Is.
	fnErr := errors.New("fn failed")
	retErr = fnErr
	releaseAndJoin(context.Background(), &fakeLock{relErr: lock.ErrLockLost}, &retErr, logger)
	if !errors.Is(retErr, fnErr) || !errors.Is(retErr, lock.ErrLockLost) {
		t.Fatalf("expected joined fn+lost error, got %v", retErr)
	}

	if buf.Len() != 0 {
		t.Fatalf("ErrLockLost must not be logged, got %q", buf.String())
	}
}

// TestReleaseAndJoin_NilLoggerFallsBackToDefault guards the nil-logger
// path: releaseAndJoin must fall back to slog.Default rather than
// dereference a nil *slog.Logger. We swap the default logger to a buffer
// so the assertion is deterministic and no output escapes to stderr.
func TestReleaseAndJoin_NilLoggerFallsBackToDefault(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var retErr error
	releaseAndJoin(context.Background(), &fakeLock{relErr: errors.New("boom")}, &retErr, nil)

	if retErr != nil {
		t.Fatalf("backend release error must not be returned, got %v", retErr)
	}
	if !strings.Contains(buf.String(), "redlock: failed to release after WithLock") {
		t.Fatalf("nil logger must fall back to slog.Default, got %q", buf.String())
	}
}

// TestWithLogger_PlumbedIntoLocker verifies WithLogger overrides the
// default logger on the constructed QuorumLocker, and that nil is
// ignored (leaving the slog.Default fallback in place) — the same
// guard redislock.WithLogger provides.
func TestWithLogger_PlumbedIntoLocker(t *testing.T) {
	custom := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	o := redsyncutil.Config{}
	WithLogger(custom)(&o)
	if o.Logger != custom {
		t.Fatalf("WithLogger must set the configured logger")
	}

	// nil must be ignored so a later default fallback can apply.
	o2 := redsyncutil.Config{Logger: custom}
	WithLogger(nil)(&o2)
	if o2.Logger != custom {
		t.Fatalf("WithLogger(nil) must not clobber an existing logger")
	}
}
