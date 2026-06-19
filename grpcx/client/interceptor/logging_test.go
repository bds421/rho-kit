package interceptor_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
)

// captureRecord is a single observed log record reduced to the fields the
// logging interceptor is contracted to emit.
type captureRecord struct {
	level slog.Level
	attrs map[string]slog.Value
}

// captureHandler is a minimal slog.Handler that records emitted records so a
// test can assert level and attributes without parsing formatted output.
type captureHandler struct {
	mu      sync.Mutex
	records *[]captureRecord
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := captureRecord{level: r.Level, attrs: map[string]slog.Value{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	*h.records = append(*h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func newCaptureLogger() (*slog.Logger, *[]captureRecord) {
	recs := &[]captureRecord{}
	return slog.New(&captureHandler{records: recs}), recs
}

func TestLoggingUnary_LogsCodeAndIDsAtInfoOnSuccess(t *testing.T) {
	logger, recs := newCaptureLogger()
	icpt := interceptor.LoggingUnary(logger)

	err := icpt(ctxWithIDs(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(*recs))
	}
	rec := (*recs)[0]
	if rec.level != slog.LevelInfo {
		t.Fatalf("level = %v, want Info on success", rec.level)
	}
	if got := rec.attrs["grpc.code"].String(); got != codes.OK.String() {
		t.Fatalf("grpc.code = %q, want %q", got, codes.OK.String())
	}
	if got := rec.attrs["grpc.method"].String(); got != "/svc/Method" {
		t.Fatalf("grpc.method = %q, want /svc/Method", got)
	}
	if got := rec.attrs["correlation_id"].String(); got != "corr-123" {
		t.Fatalf("correlation_id = %q, want corr-123", got)
	}
	if got := rec.attrs["request_id"].String(); got != "req-456" {
		t.Fatalf("request_id = %q, want req-456", got)
	}
	if _, ok := rec.attrs["duration"]; !ok {
		t.Fatalf("duration attr missing")
	}
}

func TestLoggingUnary_WarnsOnUnexpectedCode(t *testing.T) {
	logger, recs := newCaptureLogger()
	icpt := interceptor.LoggingUnary(logger)

	_ = icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return status.Error(codes.Internal, "kaboom")
		},
	)
	if len(*recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(*recs))
	}
	rec := (*recs)[0]
	if rec.level != slog.LevelWarn {
		t.Fatalf("level = %v, want Warn on Internal", rec.level)
	}
	if got := rec.attrs["grpc.code"].String(); got != codes.Internal.String() {
		t.Fatalf("grpc.code = %q, want %q", got, codes.Internal.String())
	}
}

// TestLoggingUnary_InfoOnCanceledAndDeadlineExceeded verifies the documented
// carve-out: Canceled / DeadlineExceeded are expected client behavior and log
// at Info, not Warn.
func TestLoggingUnary_InfoOnCanceledAndDeadlineExceeded(t *testing.T) {
	for _, c := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
		logger, recs := newCaptureLogger()
		icpt := interceptor.LoggingUnary(logger)
		_ = icpt(context.Background(), "/svc/Method", nil, nil, nil,
			func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				return status.Error(c, "expected")
			},
		)
		if len(*recs) != 1 {
			t.Fatalf("code %v: got %d records, want 1", c, len(*recs))
		}
		if lvl := (*recs)[0].level; lvl != slog.LevelInfo {
			t.Fatalf("code %v: level = %v, want Info", c, lvl)
		}
	}
}

// TestLoggingUnary_OmitsIDsWhenAbsent verifies that correlation/request ID
// attrs are not emitted when the ctx carries neither.
func TestLoggingUnary_OmitsIDsWhenAbsent(t *testing.T) {
	logger, recs := newCaptureLogger()
	icpt := interceptor.LoggingUnary(logger)
	_ = icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return nil
		},
	)
	rec := (*recs)[0]
	if _, ok := rec.attrs["correlation_id"]; ok {
		t.Fatalf("correlation_id should be omitted when absent on ctx")
	}
	if _, ok := rec.attrs["request_id"]; ok {
		t.Fatalf("request_id should be omitted when absent on ctx")
	}
}

func TestLoggingStream_LogsOnConstruction(t *testing.T) {
	logger, recs := newCaptureLogger()
	icpt := interceptor.LoggingStream(logger)

	ctx := contextutil.SetCorrelationID(context.Background(), "corr-789")
	_, err := icpt(ctx, &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return &fakeClientStream{ctx: ctx}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(*recs))
	}
	rec := (*recs)[0]
	if rec.level != slog.LevelInfo {
		t.Fatalf("level = %v, want Info on stream construction success", rec.level)
	}
	if got := rec.attrs["grpc.method"].String(); got != "/svc/Stream" {
		t.Fatalf("grpc.method = %q, want /svc/Stream", got)
	}
	if got := rec.attrs["correlation_id"].String(); got != "corr-789" {
		t.Fatalf("correlation_id = %q, want corr-789", got)
	}
}

func TestLoggingStream_WarnsOnConstructionError(t *testing.T) {
	logger, recs := newCaptureLogger()
	icpt := interceptor.LoggingStream(logger)

	_, _ = icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, status.Error(codes.Unavailable, "no backend")
		},
	)
	if len(*recs) != 1 {
		t.Fatalf("got %d log records, want 1", len(*recs))
	}
	if lvl := (*recs)[0].level; lvl != slog.LevelWarn {
		t.Fatalf("level = %v, want Warn on Unavailable", lvl)
	}
}
