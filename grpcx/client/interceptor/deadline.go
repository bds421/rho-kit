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

// DeadlineStream returns a stream client interceptor that bounds stream
// setup time (the streamer call) with d. Stream RPCs after setup are
// not deadline-bounded by this interceptor — long-lived bidirectional
// streams must manage their own ctx cancellation. Mirrors server-side
// DeadlineStream's "bound the setup, not the body" semantics.
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
		// For server-streaming RPCs we keep the bounded ctx (it bounds
		// the whole stream). For bidi / client-streaming we still bound
		// setup, but the kit's pattern is to apply the deadline at the
		// outer call site; document loudly in the package doc.
		ctx, cancel := boundedCtx(ctx, d)
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			cancel()
			return nil, err
		}
		return &boundedClientStream{ClientStream: cs, cancel: cancel}, nil
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
type boundedClientStream struct {
	grpc.ClientStream
	cancel context.CancelFunc
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
	}
	return err
}
