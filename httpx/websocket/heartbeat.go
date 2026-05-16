package websocket

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// heartbeatConn is the subset of [*Conn] used by the heartbeat
// goroutine. Defined as an interface so the timeout-driven close path
// is testable with a stub that does not require a real WebSocket
// peer.
type heartbeatConn interface {
	Ping(ctx context.Context) error
	Close(code StatusCode, reason string) error
}

// runHeartbeat drives the idle-keepalive ping/pong loop configured by
// [WithPingInterval] and [WithPongTimeout]. It returns when ctx is
// done or when a ping deadline expires (in which case it closes the
// connection with [StatusPolicyViolation] before returning).
//
// The function is a no-op when interval is non-positive so the caller
// does not need to guard the spawn site.
func runHeartbeat(
	ctx context.Context,
	conn heartbeatConn,
	interval, pongTimeout time.Duration,
	logger *slog.Logger,
	metrics *Metrics,
) {
	if interval <= 0 {
		return
	}
	if pongTimeout <= 0 {
		pongTimeout = interval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, cancel := context.WithTimeout(ctx, pongTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				// Re-check the parent context so a teardown that
				// happens to race with a tick is classified as a
				// graceful exit, not a pong-deadline expiry. Without
				// this, normal handler shutdown intermittently
				// double-counts as a ping timeout and closes the
				// already-closing connection with PolicyViolation.
				select {
				case <-ctx.Done():
					return
				default:
				}
				metrics.observePing(pingResultTimeout)
				if logger != nil {
					logger.WarnContext(ctx,
						"websocket: ping deadline exceeded; closing connection",
						redact.Error(err),
					)
				}
				_ = conn.Close(StatusPolicyViolation, "ping timeout")
				return
			}
			metrics.observePing(pingResultOK)
		}
	}
}
