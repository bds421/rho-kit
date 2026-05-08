package interceptor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

func TestLoggingUnary_LogsMethod(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.LoggingUnary(logger),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	_, _ = client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	var logEntry map[string]any
	err = json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "grpc request", logEntry["msg"])
	assert.Contains(t, logEntry["grpc.method"], "/grpc.health.v1.Health/Check")
}

func TestLoggingUnary_ExtractsCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.LoggingUnary(logger),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"x-correlation-id", "test-corr-123",
		"x-request-id", "test-req-456",
	)

	client := healthpb.NewHealthClient(conn)
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})

	var logEntry map[string]any
	err = json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "test-corr-123", logEntry["correlation_id"])
	assert.Equal(t, "test-req-456", logEntry["request_id"])
}

func TestLoggingUnary_NilLogger(t *testing.T) {
	i := interceptor.LoggingUnary(nil)
	assert.NotNil(t, i)
}

func TestLoggingStream_NilLogger(t *testing.T) {
	i := interceptor.LoggingStream(nil)
	assert.NotNil(t, i)
}

// TestExtractIDs_RejectsControlChars verifies that an incoming
// correlation/request ID containing control characters (newline, etc.) is
// NOT adopted into the context — instead, a fresh ID is generated. This
// closes M-5 (gRPC log-injection asymmetry vs HTTP middleware).
//
// The test injects metadata with poisoned values directly via
// metadata.NewIncomingContext (rather than AppendToOutgoingContext through
// a client), because gRPC's HTTP/2 framing rejects control bytes in
// metadata at the transport layer. The vulnerability we're closing is the
// case where a non-conforming client or proxy already managed to land
// such a value in the server's incoming metadata: the server-side
// validator must independently reject it.
func TestExtractIDs_RejectsControlChars(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const poisoned = "abc\ndef"
	md := metadata.New(map[string]string{
		"x-correlation-id": poisoned,
		"x-request-id":     poisoned,
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	ic := interceptor.LoggingUnary(logger)

	var observedCID, observedRID string
	_, err := ic(
		ctx,
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(handlerCtx context.Context, _ any) (any, error) {
			observedCID = contextutil.CorrelationID(handlerCtx)
			observedRID = contextutil.RequestID(handlerCtx)
			return nil, nil
		},
	)
	require.NoError(t, err)

	assert.NotEqual(t, poisoned, observedCID,
		"poisoned correlation_id was adopted — expected fresh ID, got %q", observedCID)
	assert.NotEqual(t, poisoned, observedRID,
		"poisoned request_id was adopted — expected fresh ID, got %q", observedRID)
	assert.NotContains(t, observedCID, "\n", "correlation_id must not contain control chars")
	assert.NotContains(t, observedRID, "\n", "request_id must not contain control chars")
	// A fresh ID is generated even when the inbound was rejected.
	assert.NotEmpty(t, observedCID, "correlation_id should be regenerated when input is invalid")
	assert.NotEmpty(t, observedRID, "request_id should be regenerated when input is invalid")

	// The fresh IDs flow into the structured log line.
	var logEntry map[string]any
	err = json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, observedCID, logEntry["correlation_id"],
		"log line should record the fresh correlation_id, not the poisoned input")
	assert.Equal(t, observedRID, logEntry["request_id"],
		"log line should record the fresh request_id, not the poisoned input")
}

func TestLoggingStream_LogsMethod(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			interceptor.LoggingStream(logger),
		),
	)
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)
	stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		_, _ = stream.Recv()
	}

	var logEntry map[string]any
	err = json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, "grpc request", logEntry["msg"])
	assert.Contains(t, logEntry["grpc.method"], "/grpc.health.v1.Health/Watch")
}
