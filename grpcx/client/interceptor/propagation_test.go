package interceptor_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
)

const (
	correlationIDHeader = "x-correlation-id"
	requestIDHeader     = "x-request-id"
)

// ctxWithIDs returns a context carrying both a correlation ID and a
// request ID via the kit contextutil setters.
func ctxWithIDs() context.Context {
	ctx := contextutil.SetCorrelationID(context.Background(), "corr-123")
	return contextutil.SetRequestID(ctx, "req-456")
}

func TestPropagationUnary_InjectsIDsIntoOutgoingMetadata(t *testing.T) {
	icpt := interceptor.PropagationUnaryClientInterceptor()
	var seen metadata.MD
	err := icpt(ctxWithIDs(), "/svc/Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			seen, _ = metadata.FromOutgoingContext(ctx)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := seen.Get(correlationIDHeader); len(got) != 1 || got[0] != "corr-123" {
		t.Fatalf("correlation-id metadata = %v, want exactly [corr-123]", got)
	}
	if got := seen.Get(requestIDHeader); len(got) != 1 || got[0] != "req-456" {
		t.Fatalf("request-id metadata = %v, want exactly [req-456]", got)
	}
}

func TestPropagationStream_InjectsIDsIntoOutgoingMetadata(t *testing.T) {
	icpt := interceptor.PropagationStreamClientInterceptor()
	var seen metadata.MD
	_, err := icpt(ctxWithIDs(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			seen, _ = metadata.FromOutgoingContext(ctx)
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := seen.Get(correlationIDHeader); len(got) != 1 || got[0] != "corr-123" {
		t.Fatalf("correlation-id metadata = %v, want exactly [corr-123]", got)
	}
	if got := seen.Get(requestIDHeader); len(got) != 1 || got[0] != "req-456" {
		t.Fatalf("request-id metadata = %v, want exactly [req-456]", got)
	}
}

// TestPropagationThenLogging_InjectsIDsExactlyOnce proves that running
// the propagation interceptor followed by the logging interceptor (the
// order client.NewClient wires them) does not double-inject the IDs.
func TestPropagationThenLogging_InjectsIDsExactlyOnce(t *testing.T) {
	prop := interceptor.PropagationUnaryClientInterceptor()
	logging := interceptor.LoggingUnary(nil)

	var seen metadata.MD
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		seen, _ = metadata.FromOutgoingContext(ctx)
		return nil
	}
	// propagation outer, logging inner — mirrors the kit chain ordering.
	err := prop(ctxWithIDs(), "/svc/Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			return logging(ctx, method, req, reply, cc, invoker, opts...)
		},
	)
	if err != nil {
		t.Fatalf("chain returned error: %v", err)
	}
	if got := seen.Get(correlationIDHeader); len(got) != 1 {
		t.Fatalf("correlation-id injected %d times, want exactly 1: %v", len(got), got)
	}
	if got := seen.Get(requestIDHeader); len(got) != 1 {
		t.Fatalf("request-id injected %d times, want exactly 1: %v", len(got), got)
	}
}
