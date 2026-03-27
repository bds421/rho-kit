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

	"github.com/bds421/rho-kit/grpcx/interceptor"
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
