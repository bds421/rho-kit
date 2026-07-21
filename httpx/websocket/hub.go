package websocket

import (
	"context"
	"net/http"
	"sync"
	"time"

	coderws "github.com/coder/websocket"
)

// Hub tracks open kit Conns created through its [Hub.Handler] so
// [Hub.Shutdown] can cancel their per-connection contexts and send
// StatusGoingAway. Use Hub when the process must drain WebSocket
// handlers on graceful shutdown; the package-level [Handle] helper
// remains for fire-and-forget endpoints that do not need coordinated
// teardown.
//
// Background: [Handle] derives each connection context via
// [context.WithoutCancel] on the request context so the upgrade
// handler's stdlib timeout does not kill long-lived sockets. That also
// severs http.Server.BaseContext cancellation, and coder/websocket
// hijacks the TCP connection so http.Server.Shutdown does not close
// them either. Hub is the non-breaking escape hatch for that gap
// (review-09).
type Hub struct {
	opts []Option

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// NewHub returns a Hub configured with the same options accepted by
// [Handle]. Options are validated eagerly so wiring bugs surface at
// construction time, matching [Handle].
func NewHub(opts ...Option) *Hub {
	_ = buildConfig(opts...)
	return &Hub{
		opts:  append([]Option(nil), opts...),
		conns: make(map[*Conn]struct{}),
	}
}

// Handler returns an http.HandlerFunc that upgrades connections and
// registers each Conn with the Hub until the application handler returns.
func (h *Hub) Handler() http.HandlerFunc {
	if h == nil {
		panic("httpx/websocket: Hub.Handler called on nil Hub")
	}
	return handleWithHooks(h.opts, h.track, h.untrack)
}

func (h *Hub) track(c *Conn) {
	if h == nil || c == nil {
		return
	}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) untrack(c *Conn) {
	if h == nil || c == nil {
		return
	}
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// Shutdown cancels every tracked connection context and closes the
// underlying sockets with StatusGoingAway. It returns when all
// registered handlers have untracked, or when ctx is done. Safe to
// call more than once; concurrent Shutdowns are serialised by the
// connection map mutex.
//
// Handlers parked on conn.Context().Done() should return promptly so
// Shutdown can complete within the process termination budget.
func (h *Hub) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	h.mu.Lock()
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	for _, c := range conns {
		c.cancelCtx()
		if !c.closed.Load() {
			c.closeOnce.Do(func() {
				c.closed.Store(true)
				c.closeCode.CompareAndSwap(0, int64(StatusGoingAway))
				_ = c.inner.Close(coderws.StatusGoingAway, "server shutdown")
				c.metrics.connClosed(int(c.closeCode.Load()))
			})
		}
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.Lock()
		n := len(h.conns)
		h.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
