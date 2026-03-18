package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/bds421/rho-kit/observability/logattr"
	"golang.org/x/sync/errgroup"
)

const (
	defaultStopTimeout = 30 * time.Second
)

// namedComponent pairs a component with a name for logging.
type namedComponent struct {
	name      string
	component Component
}

// Runner composes multiple components into a single lifecycle.
// Components are started concurrently. If any component returns a non-nil
// error, all others are stopped. On SIGINT/SIGTERM, all components are
// stopped gracefully.
type Runner struct {
	logger      *slog.Logger
	components  []namedComponent
	stopTimeout time.Duration
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithStopTimeout sets the maximum time allowed for each component's Stop
// method. Default is 30 seconds.
func WithStopTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) {
		if d > 0 {
			r.stopTimeout = d
		}
	}
}

// NewRunner creates a Runner with the given logger.
func NewRunner(logger *slog.Logger, opts ...RunnerOption) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Runner{
		logger:      logger,
		stopTimeout: defaultStopTimeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Add registers a named component. Components are started in registration
// order but run concurrently.
func (r *Runner) Add(name string, c Component) *Runner {
	r.components = append(r.components, namedComponent{name: name, component: c})
	return r
}

// AddFunc registers a function as a component. The function should block
// until ctx is cancelled.
func (r *Runner) AddFunc(name string, fn func(ctx context.Context) error) *Runner {
	return r.Add(name, &FuncComponent{StartFn: fn})
}

// Run starts all components and blocks until a signal is received or a
// component returns an error. On shutdown, all components are stopped in
// reverse registration order.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Listen for a second signal during shutdown — operators expect a second
	// Ctrl+C to force-kill the process. The first signal cancels ctx above;
	// a second signal cancels the stop timeout context.
	//
	// forceCtx wraps context.Background() and is passed to stopAll, so
	// cancelling it actually interrupts the stop timeouts (unlike cancelling
	// the already-cancelled signal ctx which is a no-op).
	//
	// The forceQuit channel is registered ONLY after ctx is cancelled to avoid
	// double-delivery (Go's signal.Notify delivers to ALL registered channels).
	// The runDone channel prevents goroutine leaks: it closes when Run returns,
	// so the goroutine exits cleanly even if no second signal arrives.
	forceCtx, forceCancel := context.WithCancel(context.Background())
	defer forceCancel()
	forceQuit := make(chan os.Signal, 1)
	runDone := make(chan struct{})
	defer close(runDone)
	go func() {
		<-ctx.Done()
		signal.Notify(forceQuit, os.Interrupt, syscall.SIGTERM)
		select {
		case <-forceQuit:
			// Second signal: cancel the stop timeout context so all pending
			// Stop calls return immediately. Unlike os.Exit(1), this still
			// allows deferred cleanup (DB close, tracing flush, file handles).
			r.logger.Error("second signal received, forcing immediate shutdown")
			forceCancel()
		case <-runDone:
		}
	}()
	defer signal.Stop(forceQuit)

	// Structured startup summary for operational visibility.
	componentNames := make([]string, len(r.components))
	for i, nc := range r.components {
		componentNames[i] = nc.name
	}
	r.logger.Info("starting components",
		slog.Int("count", len(r.components)),
		slog.Any("components", componentNames),
	)

	eg, gCtx := errgroup.WithContext(ctx)

	for _, nc := range r.components {
		eg.Go(func() (retErr error) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := string(debug.Stack())
					retErr = fmt.Errorf("goroutine %q panicked: %v", nc.name, rec)
					r.logger.Error("goroutine panicked",
						logattr.Component(nc.name),
						slog.Any("panic", rec),
						slog.String("stack", stack),
					)
				}
			}()

			r.logger.Info("component starting", logattr.Component(nc.name))

			err := nc.component.Start(gCtx)

			// http.ErrServerClosed is expected during shutdown.
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		})
	}

	// Wait for context cancellation (signal or component error), then stop
	// components in reverse order.
	eg.Go(func() error {
		<-gCtx.Done()
		r.logger.Info("shutting down components")
		return r.stopAll(forceCtx)
	})

	return eg.Wait()
}

// stopAll stops all components sequentially in reverse registration order.
// Components that have not yet finished starting will still receive Stop —
// implementations must handle this gracefully (FuncComponent.Stop is a no-op;
// http.Server.Shutdown is safe to call before ListenAndServe).
// The parent context allows force-cancellation (second signal) to interrupt
// all pending stop timeouts immediately.
// Returns all stop errors joined together.
func (r *Runner) stopAll(parent context.Context) error {
	start := time.Now()

	// Apply a single shared deadline for the entire shutdown sequence.
	// This prevents O(N × stopTimeout) total shutdown time which would
	// exceed Kubernetes terminationGracePeriodSeconds for multi-component
	// services. Components are still stopped in reverse order (sequential)
	// to preserve dependency ordering, but they share one deadline.
	sharedCtx, sharedCancel := context.WithTimeout(parent, r.stopTimeout)
	defer sharedCancel()

	var errs []error
	for i := len(r.components) - 1; i >= 0; i-- {
		if sharedCtx.Err() != nil {
			errs = append(errs, fmt.Errorf("shutdown deadline exceeded, skipping remaining components"))
			break
		}
		if err := r.stopOne(sharedCtx, r.components[i]); err != nil {
			errs = append(errs, err)
		}
	}
	joinedErr := errors.Join(errs...)
	r.logger.Info("shutdown complete",
		slog.Duration("duration", time.Since(start)),
		slog.Bool("clean", joinedErr == nil),
	)
	return joinedErr
}

// stopOne stops a single component with a timeout derived from the parent
// context. A force-quit cancels parent, which immediately cancels the stop
// timeout. Both the context cancel and panic recovery are deferred to prevent
// a panicking Stop from crashing the entire shutdown sequence.
func (r *Runner) stopOne(parent context.Context, nc namedComponent) (retErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("component stop panicked",
				logattr.Component(nc.name),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			retErr = fmt.Errorf("component %q stop panicked: %v", nc.name, rec)
		}
	}()

	if err := nc.component.Stop(parent); err != nil {
		r.logger.Error("component stop error",
			logattr.Component(nc.name),
			logattr.Error(err),
		)
		return err
	}
	r.logger.Info("component stopped", logattr.Component(nc.name))
	return nil
}
