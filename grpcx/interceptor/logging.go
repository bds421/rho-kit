package interceptor

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
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

// maxIDLen is the maximum length for an incoming correlation/request ID
// metadata value, mirroring httpx/middleware/internal/idutil's caller limit.
const maxIDLen = 128

// extractIDs reads correlation and request IDs from gRPC metadata and stores
// them in the context. Incoming values are validated with [isValidID]:
// IDs containing control characters or non-printable bytes (the classic
// log-injection vector) are treated as absent. When an ID is absent or
// invalid, a fresh ID is generated via contextutil.NewID so downstream
// logs always carry a stable identifier rather than the attacker's
// payload. Mirrors the HTTP correlationid/requestid middleware behaviour.
func extractIDs(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	ctx = adoptOrGenerate(ctx, md, correlationIDKey, contextutil.SetCorrelationID)
	ctx = adoptOrGenerate(ctx, md, requestIDKey, contextutil.SetRequestID)
	return ctx
}

// adoptOrGenerate reads the metadata value at key, validates it, and either
// adopts it onto ctx via setter or generates a fresh ID.
func adoptOrGenerate(
	ctx context.Context,
	md metadata.MD,
	key string,
	setter func(context.Context, string) context.Context,
) context.Context {
	id := ""
	if md != nil {
		if vals := md.Get(key); len(vals) > 0 {
			id = vals[0]
		}
	}
	if !isValidID(id) {
		id = contextutil.NewID()
	}
	return setter(ctx, id)
}

// isValidID returns true if id is non-empty, within length limits, and
// contains only printable ASCII characters excluding space (0x21..0x7E).
// Mirrors httpx/middleware/internal/idutil.IsValid so HTTP and gRPC apply
// the same control-character rejection rule on incoming IDs.
func isValidID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= 0x20 || c > 0x7E {
			return false
		}
	}
	return true
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
