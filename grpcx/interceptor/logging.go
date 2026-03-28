package interceptor

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/contextutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// correlationIDKey is the metadata key for correlation ID propagation.
	correlationIDKey = "x-correlation-id"

	// requestIDKey is the metadata key for request ID propagation.
	requestIDKey = "x-request-id"
)

// LoggingUnary returns a unary server interceptor that logs each RPC with
// method, status code, duration, and correlation ID (if present in metadata).
//
// The interceptor extracts correlation and request IDs from incoming gRPC
// metadata and injects them into the context for downstream use.
func LoggingUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = extractIDs(ctx)
		start := time.Now()
		resp, err := handler(ctx, req)
		logCall(ctx, logger, info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

// LoggingStream returns a stream server interceptor that logs each stream RPC.
func LoggingStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := extractIDs(ss.Context())
		wrapped := &contextStream{ServerStream: ss, ctx: ctx}
		start := time.Now()
		err := handler(srv, wrapped)
		logCall(ctx, logger, info.FullMethod, err, time.Since(start))
		return err
	}
}

// extractIDs reads correlation and request IDs from gRPC metadata and stores
// them in the context.
func extractIDs(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	if vals := md.Get(correlationIDKey); len(vals) > 0 && vals[0] != "" {
		ctx = contextutil.SetCorrelationID(ctx, vals[0])
	}
	if vals := md.Get(requestIDKey); len(vals) > 0 && vals[0] != "" {
		ctx = contextutil.SetRequestID(ctx, vals[0])
	}
	return ctx
}

// logCall logs a completed RPC call with structured attributes.
func logCall(ctx context.Context, logger *slog.Logger, method string, err error, duration time.Duration) {
	code := "OK"
	level := slog.LevelInfo
	if err != nil {
		st, ok := status.FromError(err)
		if ok {
			code = st.Code().String()
		} else {
			code = "Unknown"
		}
		level = slog.LevelWarn
	}

	attrs := []slog.Attr{
		slog.String("grpc.method", method),
		slog.String("grpc.code", code),
		slog.Duration("duration", duration),
	}

	if cid := contextutil.CorrelationID(ctx); cid != "" {
		attrs = append(attrs, slog.String("correlation_id", cid))
	}
	if rid := contextutil.RequestID(ctx); rid != "" {
		attrs = append(attrs, slog.String("request_id", rid))
	}

	logger.LogAttrs(ctx, level, "grpc request", attrs...)
}

// contextStream wraps a grpc.ServerStream to override the context.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context {
	return s.ctx
}
