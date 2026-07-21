package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// lifecycleState is the shared Start/Stop state machine for [Limiter] and
// [KeyedLimiter]. Extracted so the stop-before-start latch, done-channel
// handoff, and panic-recovering cleanup ticker cannot drift between the
// two types (review-08).
type lifecycleState struct {
	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	doneCh  chan struct{}
}

// beginStart validates the lifecycle and arms cancel/doneCh. On success the
// caller must run the cleanup loop against the returned context and rely on
// the defer close(done) that beginStart expects the caller to install via
// [lifecycleState.runCleanupLoop].
func (s *lifecycleState) beginStart(ctx context.Context, kind string) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("ratelimit: " + kind + ".Start requires a non-nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, errors.New("ratelimit: " + kind + ".Start already started")
	}
	if s.stopped {
		// Stop ran before Start and latched stopped=true. Launching the
		// cleanup loop now would orphan a goroutine the prior Stop already
		// promised to wait on. Reject, mirroring lifecycle.FuncComponent.
		return nil, errors.New("ratelimit: " + kind + ".Start already stopped")
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.doneCh = make(chan struct{})
	return runCtx, nil
}

// stop cancels the cleanup goroutine launched by beginStart/runCleanupLoop
// and waits for it to exit. Idempotent; calls before Start, after the
// goroutine has already exited, or after a prior Stop are no-ops.
func (s *lifecycleState) stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.started || s.stopped {
		s.stopped = true
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cancel := s.cancel
	done := s.doneCh
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runCleanupLoop blocks until runCtx is cancelled, ticking cleanup on the
// shared cleanupInterval cadence. Panic recovery around cleanup keeps one
// bad sweep from killing the loop. Closes doneCh on exit.
func (s *lifecycleState) runCleanupLoop(runCtx context.Context, window time.Duration, name, panicMsg string, cleanup func()) {
	done := s.doneCh
	cancel := s.cancel
	defer close(done)
	defer cancel()

	ticker := time.NewTicker(cleanupInterval(window))
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error(panicMsg,
							slog.String("limiter", name),
							redact.Panic(r),
						)
					}
				}()
				cleanup()
			}()
		}
	}
}
