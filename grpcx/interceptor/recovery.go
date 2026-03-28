package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryUnary returns a unary server interceptor that recovers from panics
// in the handler and returns codes.Internal to the client. The panic value
// and stack trace are logged at Error level.
//
// This interceptor should be the outermost (first) in the chain so it catches
// panics from all subsequent interceptors and the handler.
func RecoveryUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				logger.ErrorContext(ctx, "grpc: panic recovered",
					slog.Any("panic", r),
					slog.String("method", info.FullMethod),
					slog.String("stack", stack),
				)
				resp = nil
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// RecoveryStream returns a stream server interceptor that recovers from panics
// in the handler and returns codes.Internal.
func RecoveryStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				logger.ErrorContext(ss.Context(), "grpc: panic recovered",
					slog.Any("panic", r),
					slog.String("method", info.FullMethod),
					slog.String("stack", stack),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}
