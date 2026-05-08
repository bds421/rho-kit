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

	"github.com/bds421/rho-kit/observability/v2/logattr"
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

	// beforeStop runs synchronously after shutdown is requested but
	// before any component's Stop is invoked. It is the hook for
	// "drain external producers" semantics: shutdown hooks added by
	// app.Builder.OnShutdown run here, with infrastructure
	// connections still live.
	beforeStop func(context.Context)
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

// WithBeforeStop registers a callback that runs synchronously after
// shutdown is requested (ctx cancelled) but before any component's
// Stop is invoked. Use this for "drain external producers" semantics:
// publish a final state, finish in-flight work that depends on the
// DB or message broker, etc. The callback ctx is the same forceCtx
// stopAll uses, so a second SIGINT cancels it.
//
// Multiple calls overwrite — only one beforeStop is supported. Wrap
// at the caller if multiple actions are needed.
func WithBeforeStop(fn func(context.Context)) RunnerOption {
	return func(r *Runner) { r.beforeStop = fn }
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
//
// Panics if name is empty or component is nil — invalid lifecycle wiring
// should fail at registration, not inside a goroutine after Run starts.
func (r *Runner) Add(name string, c Component) *Runner {
	if name == "" {
		panic("lifecycle: Runner.Add requires a non-empty name")
	}
	if c == nil {
		panic("lifecycle: Runner.Add requires a non-nil component")
	}
	r.components = append(r.components, namedComponent{name: name, component: c})
	return r
}

// AddFunc registers a function as a component. The function should block
// until ctx is cancelled.
//
// Panics if name is empty or fn is nil.
func (r *Runner) AddFunc(name string, fn func(ctx context.Context) error) *Runner {
	if fn == nil {
		panic("lifecycle: Runner.AddFunc requires a non-nil function")
	}
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
		// First select: wait for signal (ctx.Done) OR for Run to finish on
		// its own (runDone). Without this, when a component returns an error
		// before any signal arrives, ctx never cancels and this goroutine
		// blocks forever on <-ctx.Done — a goroutine leak per failed Run
		// call. Selecting on runDone here lets the goroutine exit cleanly
		// in that path.
		select {
		case <-ctx.Done():
			// Fall through to register the second-signal handler below.
		case <-runDone:
			return
		}
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

	// Trigger stopAll inside the errgroup so components actually receive
	// Stop calls (otherwise long-running Start methods block forever even
	// after ctx is cancelled). Capture its error in stopErr instead of
	// returning it from the errgroup goroutine: errgroup.Wait returns only
	// the first non-nil error, which would silently drop start-side errors
	// when stopAll also errors (or vice versa). Joining both at the end
	// surfaces the full picture to the operator.
	var stopErr error
	eg.Go(func() error {
		<-gCtx.Done()
		// Run BeforeStop synchronously while components are still
		// live — hooks rely on DB / broker connections being open.
		// Any panic is converted to a structured error log so a
		// misbehaving hook cannot block component teardown.
		if r.beforeStop != nil {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						r.logger.Error("BeforeStop panicked",
							slog.Any("panic", rec),
							slog.String("stack", string(debug.Stack())),
						)
					}
				}()
				r.beforeStop(forceCtx)
			}()
		}
		r.logger.Info("shutting down components")
		stopErr = r.stopAll(forceCtx)
		return nil
	})

	startErr := eg.Wait()
	return errors.Join(startErr, stopErr)
}

// stopAll stops all components sequentially in reverse registration order.
// Components that have not yet finished starting will still receive Stop —
// implementations must handle this gracefully (FuncComponent.Stop is a no-op;
// http.Server.Shutdown is safe to call before ListenAndServe).
// The parent context allows force-cancellation (second signal) to interrupt
// all pending stop timeouts immediately.
//
// Each component receives a per-component minimum budget so a slow earlier
// component cannot starve later ones. The total budget is still bounded by
// stopTimeout, but the per-step budget is min(stopTimeout/N, perStepCap)
// — within that, every component gets a chance to run.
//
// Returns all stop errors joined together.
func (r *Runner) stopAll(parent context.Context) error {
	start := time.Now()

	sharedCtx, sharedCancel := context.WithTimeout(parent, r.stopTimeout)
	defer sharedCancel()

	// Per-component minimum budget: stopTimeout / componentCount, capped at
	// 5s. This prevents a slow first component from consuming the entire
	// budget while still respecting the overall stopTimeout via sharedCtx.
	n := len(r.components)
	if n == 0 {
		return nil
	}
	perStep := r.stopTimeout / time.Duration(n)
	if perStep > 5*time.Second {
		perStep = 5 * time.Second
	}

	var errs []error
	for i := n - 1; i >= 0; i-- {
		if sharedCtx.Err() != nil {
			r.logger.Warn("shutdown deadline exceeded, skipping remaining component",
				logattr.Component(r.components[i].name))
			errs = append(errs, fmt.Errorf("shutdown deadline exceeded; skipped %q", r.components[i].name))
			continue
		}
		stepCtx, stepCancel := context.WithTimeout(sharedCtx, perStep)
		if err := r.stopOne(stepCtx, r.components[i]); err != nil {
			errs = append(errs, err)
		}
		stepCancel()
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
