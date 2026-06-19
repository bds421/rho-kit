// Package interceptor — deadline.go: per-RPC default deadline.
//
// Without a server-side default deadline, a streaming or unary RPC
// from a misbehaving client (or a client that crashed mid-call) can
// hold a server-side handler context open indefinitely. Goroutines
// piling up against a slow downstream dependency exhaust the
// goroutine pool and cascade to liveness failure — exactly the
// "streaming-RPC exhaustion" gap GAP-03 in
// docs/audit/THREAT_MODEL.md.
//
// DeadlineUnary / DeadlineStream wrap each RPC's ctx with a
// WithTimeout when:
//
//   - the inbound request did not already carry a deadline; or
//   - the inbound deadline is further out than the configured
//     default (the kit's deadline becomes the upper bound).
//
// Clients that legitimately need longer than the default override on
// a per-call basis (typically already documented per-method).

package interceptor

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DeadlineUnary returns a unary server interceptor that enforces a
// default per-RPC deadline. d must be positive.
func DeadlineUnary(d time.Duration) grpc.UnaryServerInterceptor {
	if d <= 0 {
		panic("grpcx/interceptor: DeadlineUnary requires a positive deadline")
	}
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, cancel := withDefaultDeadline(ctx, d)
		defer cancel()
		return handler(ctx, req)
	}
}

// DeadlineStream returns a stream server interceptor that enforces a
// default per-RPC deadline. d must be positive.
//
// The deadline is cooperative: it only tightens the context returned
// by the wrapped stream's Context(). A handler that does not propagate
// or check that context — e.g. one blocked in ServerStream.RecvMsg on
// the underlying transport — never observes the derived deadline,
// because context expiry does not abort an in-flight transport read.
// For non-cooperative handlers, pair this with gRPC keepalive
// enforcement (see [grpcx.WithKeepalivePolicy]) so a stalled or crashed
// client connection is torn down at the transport level.
func DeadlineStream(d time.Duration) grpc.StreamServerInterceptor {
	if d <= 0 {
		panic("grpcx/interceptor: DeadlineStream requires a positive deadline")
	}
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, cancel := withDefaultDeadline(ss.Context(), d)
		defer cancel()
		return handler(srv, &deadlineStream{ServerStream: ss, ctx: ctx})
	}
}

// withDefaultDeadline tightens ctx to deadline `now+d` ONLY when the
// inbound ctx has no deadline at all OR has a deadline further out
// than `now+d`. Returning a no-op cancel keeps the deferred-cancel
// pattern uniform regardless of branch.
func withDefaultDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	defaultUntil := time.Now().Add(d)
	if dl, ok := ctx.Deadline(); ok && !dl.After(defaultUntil) {
		// Caller's deadline is already at or before our cap — leave
		// it alone. Cancel is a no-op so the caller can still defer.
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, defaultUntil)
}

// deadlineStream wraps grpc.ServerStream so handler-side ctx reads
// see the tightened deadline. All other ServerStream methods
// delegate.
type deadlineStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *deadlineStream) Context() context.Context { return s.ctx }
