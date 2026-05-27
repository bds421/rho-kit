package interceptor

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

// Metadata key names mirror the server-side interceptor so
// correlation/request IDs propagate end-to-end across hops.
const (
	correlationIDKey = "x-correlation-id"
	requestIDKey     = "x-request-id"
)

// LoggingUnary returns a unary client interceptor that logs each
// completed call with method, status code, and duration. Propagates
// kit correlation_id + request_id from the caller's ctx to the wire
// metadata so the server-side LoggingUnary sees them.
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
		ctx = injectIDs(ctx)
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
		ctx = injectIDs(ctx)
		start := time.Now()
		cs, err := streamer(ctx, desc, cc, method, opts...)
		logCall(ctx, logger, method, err, time.Since(start))
		return cs, err
	}
}

// injectIDs copies kit correlation/request IDs from ctx into outgoing
// metadata so the server side sees them. If the ID is not present on
// ctx, nothing is added — the server's adoptOrGenerate will allocate.
func injectIDs(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	if md == nil {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	if cid := contextutil.CorrelationID(ctx); cid != "" && len(md.Get(correlationIDKey)) == 0 {
		md.Set(correlationIDKey, cid)
	}
	if rid := contextutil.RequestID(ctx); rid != "" && len(md.Get(requestIDKey)) == 0 {
		md.Set(requestIDKey, rid)
	}
	return metadata.NewOutgoingContext(ctx, md)
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
