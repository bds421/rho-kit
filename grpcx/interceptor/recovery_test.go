package interceptor_test

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx/interceptor"
)

const bufSize = 1024 * 1024

// panicHealthServer is a health server that panics on Check.
type panicHealthServer struct {
	healthpb.UnimplementedHealthServer
}

func (p *panicHealthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	panic("test panic")
}

func TestRecoveryUnary_CatchesPanic(t *testing.T) {
	lis := bufconn.Listen(bufSize)

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.RecoveryUnary(slog.Default()),
		),
	)
	healthpb.RegisterHealthServer(srv, &panicHealthServer{})

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
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.Equal(t, "internal error", st.Message())
}

func TestRecoveryUnary_NoPanic(t *testing.T) {
	lis := bufconn.Listen(bufSize)

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			interceptor.RecoveryUnary(slog.Default()),
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
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	// UnimplementedHealthServer returns Unimplemented, not a panic
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestRecoveryUnary_NilLogger(t *testing.T) {
	// Should not panic with nil logger.
	i := interceptor.RecoveryUnary(nil)
	assert.NotNil(t, i)
}

func TestRecoveryStream_NilLogger(t *testing.T) {
	i := interceptor.RecoveryStream(nil)
	assert.NotNil(t, i)
}
