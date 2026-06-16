package interceptor

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

// LoggingUnary returns a unary client interceptor that logs each
// completed call with method, status code, and duration.
//
// Correlation/request-ID propagation onto the wire is handled by the
// always-on [PropagationUnaryClientInterceptor] (wired ahead of logging
// in [client.NewClient]), so logging can be disabled without severing
// end-to-end trace joins.
//
// Level: Info on OK / Canceled / DeadlineExceeded (expected), Warn on
// every other code. Canceled / DeadlineExceeded are not warnings
// because clients legitimately abandon RPCs.
func LoggingUnary(logger *slog.Logger) grpc.UnaryClientInterceptor {
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
	) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		logCall(ctx, logger, method, err, time.Since(start))
		return err
	}
}

// LoggingStream returns a stream client interceptor mirroring
// [LoggingUnary]. The log line fires on stream construction; the kit
// does not log per-message events to avoid log-volume blow-up on
// chatty bidi streams.
func LoggingStream(logger *slog.Logger) grpc.StreamClientInterceptor {
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
	) (grpc.ClientStream, error) {
		start := time.Now()
		cs, err := streamer(ctx, desc, cc, method, opts...)
		logCall(ctx, logger, method, err, time.Since(start))
		return cs, err
	}
}

func logCall(ctx context.Context, logger *slog.Logger, method string, err error, duration time.Duration) {
	code := codeName(err)
	level := slog.LevelInfo
	if err != nil {
		if c := codeOf(err); c != codes.Canceled && c != codes.DeadlineExceeded {
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
	logger.LogAttrs(ctx, level, "grpc client call", attrs...)
}

func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

func codeName(err error) string {
	return codeOf(err).String()
}
