package lifecycle

import (
	"context"
	"net/http"
	"sync"
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

// HTTPServer adapts an *http.Server to a Component.
// Start calls ListenAndServe (or ListenAndServeTLS if TLSConfig is set).
// Stop calls Shutdown for graceful draining.
// Panics if srv is nil.
func HTTPServer(srv *http.Server) Component {
	if srv == nil {
		panic("lifecycle: HTTPServer requires a non-nil *http.Server")
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
	return h.srv.Shutdown(ctx)
}

// FuncComponent adapts a simple function to the Component interface.
// The function should block until ctx is cancelled.
//
// Stop() cancels the context passed to StartFn and waits for StartFn to
// return (up to the stop context deadline). This ensures the Runner's
// stopTimeout is enforceable and the function has fully cleaned up before
// shutdown proceeds.
type FuncComponent struct {
	StartFn func(ctx context.Context) error
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{} // closed when StartFn returns
}

func (f *FuncComponent) Start(ctx context.Context) error {
	if f.StartFn == nil {
		panic("lifecycle: FuncComponent.StartFn must not be nil")
	}
	ctx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	f.cancel = cancel
	f.done = make(chan struct{})
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		close(f.done)
		f.mu.Unlock()
	}()
	return f.StartFn(ctx)
}

func (f *FuncComponent) Stop(ctx context.Context) error {
	f.mu.Lock()
	cancel := f.cancel
	done := f.done
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
