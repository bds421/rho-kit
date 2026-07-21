package websocket

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Hub tracks open kit Conns created through its [Hub.Handler] so
// [Hub.Shutdown] can cancel their per-connection contexts and send
// StatusGoingAway.
//
// Prefer package-level [Handle] + [Shutdown] for a single process that
// drains all WebSocket handlers on stop. Use Hub when multiple
// independent servers or test fixtures must isolate connection
// registries so package [Shutdown] does not affect them (and vice
// versa).
//
// Background: both [Handle] and Hub derive each connection context via
// [context.WithoutCancel] on the request context so the upgrade
// handler's stdlib timeout does not kill long-lived sockets. That also
// severs http.Server.BaseContext cancellation, and coder/websocket
// hijacks the TCP connection so http.Server.Shutdown does not close
// them either. Package [Shutdown] / [Hub.Shutdown] is the drain path
// for that gap (review-09).
type Hub struct {
	opts []Option

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// NewHub returns a Hub configured with the same options accepted by
// [Handle]. Options are validated eagerly so wiring bugs surface at
// construction time, matching [Handle]. The Hub's connection registry
// is independent of the package-level registry used by [Handle]/
// [Shutdown].
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
// call more than once or concurrently.
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

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	closing := make(map[*Conn]struct{})
	for {
		h.mu.Lock()
		conns := make([]*Conn, 0, len(h.conns))
		for c := range h.conns {
			conns = append(conns, c)
		}
		h.mu.Unlock()
		if len(conns) == 0 {
			return nil
		}
		for _, c := range conns {
			if _, started := closing[c]; started {
				continue
			}
			closing[c] = struct{}{}
			// Keep the shutdown close path identical to the exported Conn API:
			// cancel first, send 1001, close once, and emit close metrics once.
			// Close may wait for an unresponsive peer's handshake, so never let
			// that wait prevent Shutdown from honoring its own context deadline.
			go func(conn *Conn) { _ = conn.Close(StatusGoingAway, "server shutdown") }(c)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
