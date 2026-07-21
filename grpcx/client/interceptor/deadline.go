package interceptor

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DeadlineUnary returns a unary client interceptor that injects a
// default per-RPC deadline of d when the caller-supplied ctx does NOT
// already carry one tighter than now+d. Mirrors the server-side
// [grpcx/interceptor.DeadlineUnary] so unary calls are bounded
// regardless of whether the caller remembered context.WithTimeout.
//
// Caller deadlines stricter than now+d are preserved.
func DeadlineUnary(d time.Duration) grpc.UnaryClientInterceptor {
	if d <= 0 {
		panic("client/interceptor: DeadlineUnary requires positive duration")
	}
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx, cancel := boundedCtx(ctx, d)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// DeadlineStream returns a stream client interceptor that bounds the
// ENTIRE stream — both setup and body — with d, unless the caller's ctx
// already carries a tighter deadline (which is preserved). The bounded
// ctx is the context the stream runs on, so once d elapses gRPC aborts
// the stream with DeadlineExceeded; this applies to server-streaming,
// client-streaming, and bidirectional RPCs alike. Mirrors the
// server-side [grpcx/interceptor.DeadlineStream], which likewise bounds
// the whole handler.
//
// IMPORTANT: long-lived streams (watches, bidi pub/sub) WILL be killed
// after d. [github.com/bds421/rho-kit/grpcx/v2/client.NewClient]
// installs this with the default deadline by default, so callers running
// long-lived streams must either set a wide enough deadline via
// [client.WithDefaultTimeout] or opt out with
// [client.WithoutDefaultDeadline].
func DeadlineStream(d time.Duration) grpc.StreamClientInterceptor {
	if d <= 0 {
		panic("client/interceptor: DeadlineStream requires positive duration")
	}
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		// boundedCtx is the ctx the stream runs on, so for every stream
		// kind (server / client / bidi) this deadline bounds the whole
		// stream, not just setup. The wrapped stream's RecvMsg fires
		// cancel on terminal error to release the timer early.
		ctx, cancel := boundedCtx(ctx, d)
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			cancel()
			return nil, err
		}
		return &boundedClientStream{
			ClientStream:  cs,
			cancel:        cancel,
			serverStreams: desc != nil && desc.ServerStreams,
		}, nil
	}
}

func boundedCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if ok && deadline.Before(time.Now().Add(d)) {
		// Caller-supplied deadline is tighter — preserve it.
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}

// boundedClientStream wraps a grpc.ClientStream so the cancel fires
// once the stream is done (either RecvMsg returns io.EOF / error, or
// the caller calls CloseSend then Recv to drain).
//
// For client-streaming / unary-response RPCs (ServerStreams=false) the
// first successful RecvMsg is terminal (CloseAndRecv), so cancel fires
// immediately rather than waiting for the timeout timer.
type boundedClientStream struct {
	grpc.ClientStream
	cancel        context.CancelFunc
	serverStreams bool
}

// CloseSend calls the underlying CloseSend and leaves cancel for the
// final RecvMsg to fire. Cancelling here would tear the stream down
// before the server's trailers arrive.
func (s *boundedClientStream) CloseSend() error {
	return s.ClientStream.CloseSend()
}

// RecvMsg cancels the bounded ctx after the stream emits its terminal
// error (io.EOF on a normal close, or a real RPC error). The cancel is
// idempotent so multiple RecvMsg calls after EOF are safe.
func (s *boundedClientStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.cancel()
		return err
	}
	// Client-streaming / unary response: one successful RecvMsg completes
	// the RPC (generated CloseAndRecv). Cancel promptly so the timeout
	// timer does not linger until d elapses.
	if !s.serverStreams {
		s.cancel()
	}
	return nil
}
