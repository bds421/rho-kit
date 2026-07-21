package interceptor

import (
	"context"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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
	) (resp any, err error) {
		ctx = extractIDs(ctx)
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				logCall(ctx, logger, info.FullMethod, status.Error(codes.Internal, "panic"), time.Since(start))
				panic(rec)
			}
			logCall(ctx, logger, info.FullMethod, err, time.Since(start))
		}()
		resp, err = handler(ctx, req)
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
	) (err error) {
		ctx := extractIDs(ss.Context())
		wrapped := &contextStream{ServerStream: ss, ctx: ctx}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				logCall(ctx, logger, info.FullMethod, status.Error(codes.Internal, "panic"), time.Since(start))
				panic(rec)
			}
			logCall(ctx, logger, info.FullMethod, err, time.Since(start))
		}()
		err = handler(srv, wrapped)
		return err
	}
}

// maxIDLen is the maximum length for an incoming correlation/request ID
// metadata value.
const maxIDLen = contextutil.MaxCorrelationIDLen

// extractIDs reads correlation and request IDs from gRPC metadata and stores
// them in the context. Incoming values are treated as absent unless exactly
// one safe correlation token is present. When an ID is absent or invalid, a
// fresh ID is generated via contextutil.GenerateID so downstream logs always carry
// a stable identifier rather than the attacker's payload. Mirrors the HTTP
// correlationid/requestid middleware behaviour.
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
		if vals := md.Get(key); len(vals) == 1 {
			id = vals[0]
		}
	}
	if !isValidID(id) {
		id = contextutil.GenerateID()
	}
	return setter(ctx, id)
}

// isValidID returns true if id is a safe request/correlation token.
func isValidID(id string) bool {
	return contextutil.IsValidCorrelationToken(id, maxIDLen)
}

// logCall logs a completed RPC call with structured attributes.
// Client-driven outcomes (Canceled, DeadlineExceeded) stay at Info —
// matching the sibling client interceptor — so browser aborts and
// legitimate timeouts do not flood Warn. Server-side failure codes use Warn.
func logCall(ctx context.Context, logger *slog.Logger, method string, err error, duration time.Duration) {
	code := "OK"
	level := slog.LevelInfo
	if err != nil {
		st, ok := status.FromError(err)
		if ok {
			code = st.Code().String()
			switch st.Code() {
			case codes.Canceled, codes.DeadlineExceeded,
				codes.InvalidArgument, codes.NotFound,
				codes.AlreadyExists, codes.FailedPrecondition,
				codes.PermissionDenied, codes.Unauthenticated:
				level = slog.LevelInfo
			default:
				level = slog.LevelWarn
			}
		} else {
			code = "Unknown"
			level = slog.LevelWarn
		}
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
