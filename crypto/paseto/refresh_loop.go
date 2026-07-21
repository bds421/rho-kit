package paseto

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// refreshLoopState is the shared tick/stop machinery for [Provider] and
// [SigningProvider]. Extracted so shutdown races, cancel-on-Close, and
// panic-recovering error callbacks cannot drift between the two types
// (review-04).
type refreshLoopState struct {
	stop       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once
	closed     atomic.Bool
	rootCtx    context.Context
	rootCancel context.CancelFunc
	interval   time.Duration
	fetchTO    time.Duration
}

func newRefreshLoopState(interval, fetchTimeout time.Duration) refreshLoopState {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return refreshLoopState{
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
		interval:   interval,
		fetchTO:    fetchTimeout,
	}
}

// loop ticks refresh until stop is closed. Errors after Close are
// suppressed so shutdown cancellation does not fire a false
// "rotation stalled" alert.
func (s *refreshLoopState) loop(refresh func(context.Context) error, onErr func(error)) {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			// Derive each refresh from rootCtx (cancelled by Close)
			// so an in-flight Close aborts the network call instead
			// of waiting for the per-refresh timeout. The per-refresh
			// timeout uses fetchTO — independent of interval — so a
			// long polling cadence does not translate into a long
			// shutdown delay (FR-046).
			ctx, cancel := context.WithTimeout(s.rootCtx, s.fetchTO)
			err := refresh(ctx)
			cancel()
			if err != nil && !s.closed.Load() {
				onErr(err)
			}
		}
	}
}

// close terminates the loop. Idempotent; blocks until loop exits.
func (s *refreshLoopState) close() {
	if s == nil || s.stop == nil || s.done == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		close(s.stop)
		if s.rootCancel != nil {
			s.rootCancel()
		}
	})
	<-s.done
}

// callRefreshError runs onErr with panic recovery. logLabel is the
// slog message prefix distinguishing verify vs signing callbacks.
func callRefreshError(onErr func(error), err error, logLabel string) {
	if onErr == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error(logLabel,
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
		}
	}()
	onErr(err)
}
