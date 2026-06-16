package websocket

import (
	"context"
	"errors"
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
// done or when a ping fails (in which case it closes the connection
// with [StatusPolicyViolation] before returning).
//
// A failed ping is recorded as pings_total{result="timeout"} only when
// the failure is the pong deadline expiring; any other ping error
// (peer reset, already-dead connection) is recorded as result="error"
// so the timeout bucket reflects genuine deadline expiry alone.
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
				// Distinguish a genuine pong-deadline expiry (the
				// pingCtx timeout fired) from ordinary connection death
				// surfaced through Ping — peer reset, or a conn already
				// closed by a racing read-error path. Only the former is
				// the timeout the metric and PolicyViolation close are
				// meant to signal; conflating the two skews the operator
				// view of pings_total{result="timeout"}.
				if errors.Is(err, context.DeadlineExceeded) {
					metrics.observePing(pingResultTimeout)
					if logger != nil {
						logger.WarnContext(ctx,
							"websocket: ping deadline exceeded; closing connection",
							redact.Error(err),
						)
					}
				} else {
					metrics.observePing(pingResultError)
					if logger != nil {
						logger.WarnContext(ctx,
							"websocket: ping failed; closing connection",
							redact.Error(err),
						)
					}
				}
				// Close is idempotent on [*Conn], so closing an
				// already-dead connection is a harmless no-op; closing
				// keeps the read loop unblocked when the conn is still up.
				_ = conn.Close(StatusPolicyViolation, "ping timeout")
				return
			}
			metrics.observePing(pingResultOK)
		}
	}
}
