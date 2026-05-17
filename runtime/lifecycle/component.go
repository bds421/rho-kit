package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Component represents anything with a start/stop lifecycle.
// Start should block until the component is done or ctx is cancelled.
// Stop performs graceful shutdown.
type Component interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// httpServerComponent adapts an *http.Server to the Component interface.
type httpServerComponent struct {
	srv *http.Server
}

// NewHTTPServer adapts an *http.Server to a Component.
// Start calls ListenAndServe (or ListenAndServeTLS if TLSConfig is set).
// Stop calls Shutdown for graceful draining.
//
// Panics if srv is nil, srv.Addr is empty, srv.Handler is nil, or
// srv.ReadHeaderTimeout is zero — all wiring mistakes caught at
// construction so a misconfigured server never reaches Start.
func NewHTTPServer(srv *http.Server) Component {
	if srv == nil {
		panic("lifecycle: NewHTTPServer requires a non-nil *http.Server")
	}
	if srv.Addr == "" {
		panic("lifecycle: NewHTTPServer requires http.Server.Addr to be set")
	}
	if srv.Handler == nil {
		panic("lifecycle: NewHTTPServer requires a non-nil Handler")
	}
	if srv.ReadHeaderTimeout <= 0 {
		panic("lifecycle: NewHTTPServer requires ReadHeaderTimeout > 0")
	}
	return &httpServerComponent{srv: srv}
}

func (h *httpServerComponent) Start(_ context.Context) error {
	if h.srv.TLSConfig != nil {
		return h.srv.ListenAndServeTLS("", "")
	}
	return h.srv.ListenAndServe()
}

func (h *httpServerComponent) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("lifecycle: NewHTTPServer.Stop requires a non-nil context")
	}
	return h.srv.Shutdown(ctx)
}

// FuncComponent adapts a simple function to the Component interface.
// The function should block until ctx is cancelled.
//
// Stop() cancels the context passed to the function and waits for it to
// return (up to the stop context deadline). This ensures the Runner's
// stopTimeout is enforceable and the function has fully cleaned up before
// shutdown proceeds.
//
// Construct via [NewFuncComponent] — the zero value is not usable.
type FuncComponent struct {
	startFn func(ctx context.Context) error
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{} // closed when startFn returns
	started bool          // set under mu; rejects re-entry
	stopped bool          // set under mu; rejects Start after Stop-before-Start
}

// NewFuncComponent wraps fn into a Component. Panics if fn is nil so the
// wiring bug surfaces at construction time, not on the first Start.
func NewFuncComponent(fn func(ctx context.Context) error) *FuncComponent {
	if fn == nil {
		panic("lifecycle: NewFuncComponent requires a non-nil function")
	}
	return &FuncComponent{startFn: fn}
}

func (f *FuncComponent) Start(ctx context.Context) (retErr error) {
	if ctx == nil {
		return errors.New("lifecycle: FuncComponent.Start requires a non-nil context")
	}
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		// Re-Start would overwrite f.done, breaking any concurrent Stop
		// awaiting the previous run's completion. FuncComponent is
		// intentionally one-shot.
		return errors.New("lifecycle: FuncComponent already started")
	}
	if f.stopped {
		f.mu.Unlock()
		return errors.New("lifecycle: FuncComponent already stopped")
	}
	f.started = true
	ctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	f.done = make(chan struct{})
	f.mu.Unlock()
	defer func() {
		// Wave 145: convert a panicking startFn into an error return.
		// Previously the panic propagated past the Runner and crashed
		// the whole service; downstream lifecycle siblings never got
		// their Stop call. The recovered value's concrete type is
		// preserved via redact.PanicValue so triage can see what
		// raised without leaking the panic payload.
		if r := recover(); r != nil {
			retErr = fmt.Errorf("lifecycle: FuncComponent panicked: %s", redact.PanicValue(r))
		}
		f.mu.Lock()
		close(f.done)
		f.mu.Unlock()
	}()
	return f.startFn(ctx)
}

func (f *FuncComponent) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("lifecycle: FuncComponent.Stop requires a non-nil context")
	}
	f.mu.Lock()
	cancel := f.cancel
	done := f.done
	f.stopped = true
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
