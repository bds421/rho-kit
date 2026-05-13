// Package temporal hosts the kit's Temporal Go SDK adapter:
// lifecycle.Component-compatible workers and a Component that
// connects to a Temporal cluster on Init and shuts down workers on
// Stop. v2 positions Temporal as the durable-workflow substrate —
// long-running, replay-able, multi-step orchestrations (saga, AI
// agent runs, multi-day flows). For "schedule a job and run it
// soon" workloads the lighter [data/queue/riverqueue] adapter is
// almost always the better fit.
//
// # Scope of guarantees
//
// FR-096 [LOW]: this package is a thin adapter, NOT a hardened
// production profile. The kit ships:
//
//   - lifecycle.Component wiring (Init/Start/Stop) so Temporal joins
//     the kit's startup/shutdown sequence.
//   - Identity + namespace defaults from [Config].
//   - A pass-through to [client.Options] so callers can plug in any
//     SDK feature.
//
// The kit does NOT take opinions on TLS, auth (mTLS / API key /
// OIDC), retry policy, worker concurrency, namespace creation, or
// task-queue isolation. Services targeting Temporal Cloud or any
// production cluster MUST configure these via the standard SDK
// options surfaced through [Config.ClientOptions] and
// [Config.WorkerOptions]; the kit's defaults are appropriate only
// for local-dev and integration tests against a permissive cluster.
//
// asvs: V11.1.1
package temporal

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Config bundles the connection knobs the kit takes opinions on.
// Anything not exposed here can be set on the [client.Options]
// returned by [Config.ClientOptions] before passing it to [Connect].
type Config struct {
	// HostPort is the Temporal frontend address (e.g. "temporal:7233").
	HostPort string
	// Namespace is the Temporal namespace the service operates in.
	Namespace string
	// Identity tags client connections in the Temporal UI. Defaults
	// to the OS hostname; override only when running multiple
	// distinct fleets in one process.
	Identity string
}

// ClientOptions builds Temporal's connection options from [Config]
// with the kit's slog-bridged logger applied. Callers add interceptors
// or TLS by editing the returned struct before calling [Connect].
func (c Config) ClientOptions(logger *slog.Logger) client.Options {
	if logger == nil {
		logger = slog.Default()
	}
	return client.Options{
		HostPort:  c.HostPort,
		Namespace: c.Namespace,
		Identity:  c.Identity,
		Logger:    bridgeLogger{l: logger},
	}
}

// Connect dials Temporal with the given options and returns a kit
// Client. The kit's wrapper exists so worker registration can hold
// onto a shared client without Temporal's option flow leaking into
// every consumer.
//
// Returns an error if the dial fails. The kit deliberately does NOT
// retry — connection failure at startup should be reported up the
// lifecycle Runner so an orchestrator can surface a CrashLoop.
func Connect(ctx context.Context, opts client.Options) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("temporal: Connect requires a non-nil context")
	}
	if opts.HostPort == "" {
		return nil, errors.New("temporal: Config.HostPort must not be empty")
	}
	if opts.Namespace == "" {
		return nil, errors.New("temporal: Config.Namespace must not be empty")
	}
	c, err := client.DialContext(ctx, opts)
	if err != nil {
		return nil, errors.New("temporal: dial failed")
	}
	return &Client{c: c}, nil
}

// Client wraps [client.Client] so the kit can attach extra
// helpers (worker registration, lifecycle integration) without
// callers reaching into the SDK directly. Callers that need the raw
// SDK client use [Client.SDK]. Client is safe for concurrent use; in
// particular [Client.Close] is idempotent and may be called from any
// goroutine.
type Client struct {
	c        client.Client
	closeOne sync.Once
}

// SDK returns the underlying SDK client. Use this for anything the
// kit doesn't wrap — workflow execution, query API, signal API.
func (c *Client) SDK() client.Client { return c.c }

// Close terminates the connection. Idempotent and goroutine-safe — the
// underlying SDK's Close is not contractually idempotent across versions,
// so we gate it behind a sync.Once instead of forwarding every call.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.closeOne.Do(func() {
		if c.c != nil {
			c.c.Close()
		}
	})
}

// Worker bundles a Temporal worker with the kit's
// lifecycle.Component contract. Register workflows and activities on
// it via [worker.Registry] (returned by [Worker.Registry]) before
// starting; once Start runs the registry is sealed.
type Worker struct {
	w        temporalWorker
	registry worker.Registry
	mu       sync.Mutex
	started  bool
	stopped  bool
	stopOnce sync.Once
}

type temporalWorker interface {
	Run(interruptCh <-chan interface{}) error
	Stop()
}

// NewWorker creates a kit Worker for the given task queue. Pass
// [worker.Options]{} to use Temporal SDK defaults; the kit doesn't
// override worker tuning because workflow throughput is highly
// service-specific.
func NewWorker(client *Client, taskQueue string, opts worker.Options) *Worker {
	if client == nil {
		panic("temporal: client must not be nil")
	}
	if taskQueue == "" {
		panic("temporal: taskQueue must not be empty")
	}
	w := worker.New(client.c, taskQueue, opts)
	return &Worker{w: w, registry: w}
}

// Registry returns the Temporal worker's registration surface so the
// caller can attach workflows / activities. Safe to call before
// Start, panics afterwards (Temporal SDK enforces the same).
func (w *Worker) Registry() worker.Registry { return w.registry }

// Start begins polling the task queue. Blocks until ctx is cancelled
// or Stop is called. Returns the worker's terminal error (nil on
// graceful shutdown).
//
// This is a [lifecycle.Component]-compatible Start: app.Builder can
// add a Worker via runner.Add(...) and the runner will manage its
// lifetime alongside HTTP / gRPC servers.
func (w *Worker) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("temporal: Worker.Start requires a non-nil context")
	}
	w.mu.Lock()
	if w.w == nil {
		w.mu.Unlock()
		return errors.New("temporal: Worker is not initialized")
	}
	if w.started {
		w.mu.Unlock()
		return errors.New("temporal: Worker already started")
	}
	if w.stopped {
		w.mu.Unlock()
		return errors.New("temporal: Worker already stopped")
	}
	w.started = true
	w.mu.Unlock()

	// The SDK's worker.Run wants a <-chan interface{} for shutdown
	// signalling (legacy interrupt-channel API). Bridge ctx onto it
	// in a goroutine so the runner's cancellation flows through
	// cleanly without exposing the SDK's chan type to callers.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	interrupt := make(chan any, 1)
	go func() {
		<-runCtx.Done()
		select {
		case interrupt <- struct{}{}:
		default:
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- w.w.Run(interrupt)
	}()
	select {
	case <-ctx.Done():
		w.stopWorker()
		return nil
	case err := <-errCh:
		return err
	}
}

// Stop halts the worker. Idempotent.
func (w *Worker) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("temporal: Worker.Stop requires a non-nil context")
	}
	w.stopWorker()
	return nil
}

func (w *Worker) stopWorker() {
	w.mu.Lock()
	ww := w.w
	w.stopped = true
	w.mu.Unlock()
	if ww != nil {
		w.stopOnce.Do(func() { ww.Stop() })
	}
}

// bridgeLogger adapts slog.Logger to the SDK's [log.Logger]
// interface. The SDK's interface is intentionally minimal so any
// structured logger plugs in cleanly.
type bridgeLogger struct{ l *slog.Logger }

func (b bridgeLogger) Debug(msg string, kv ...any) { b.l.Debug(msg, kv...) }
func (b bridgeLogger) Info(msg string, kv ...any)  { b.l.Info(msg, kv...) }
func (b bridgeLogger) Warn(msg string, kv ...any)  { b.l.Warn(msg, kv...) }
func (b bridgeLogger) Error(msg string, kv ...any) { b.l.Error(msg, kv...) }
