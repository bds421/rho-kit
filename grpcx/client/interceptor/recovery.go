package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// RecoveryUnary returns a unary client interceptor that recovers from
// panics in caller-supplied client interceptors (mistakes in custom
// retry/auth code) and converts them to codes.Internal so the caller
// sees a structured error instead of a process crash.
//
// Without this, a panic in a custom interceptor unwinds straight
// through the kit's chain and takes down the goroutine; in a streaming
// RPC that also leaks the open stream context.
func RecoveryUnary(logger *slog.Logger) grpc.UnaryClientInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "grpc client interceptor panic",
					slog.String("grpc.method", method),
					redact.Panic(r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "client interceptor panicked")
			}
		}()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// RecoveryStream returns a stream client interceptor that recovers
// panics from the chain. See [RecoveryUnary] for rationale.
func RecoveryStream(logger *slog.Logger) grpc.StreamClientInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (cs grpc.ClientStream, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "grpc client stream interceptor panic",
					slog.String("grpc.method", method),
					redact.Panic(r),
					slog.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "client interceptor panicked")
			}
		}()
		return streamer(ctx, desc, cc, method, opts...)
	}
}
