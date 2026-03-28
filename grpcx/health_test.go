package grpcx_test

import (
	"context"
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

	"github.com/bds421/rho-kit/grpcx"
	"github.com/bds421/rho-kit/observability/health"
)

const bufSize = 1024 * 1024

func TestHealthServer_Healthy(t *testing.T) {
	checker := &health.Checker{
		Version: "1.0.0",
		Checks: []health.DependencyCheck{
			{
				Name:     "test-db",
				Critical: true,
				Check: func(context.Context) string {
					return health.StatusHealthy
				},
			},
		},
	}

	hs := grpcx.NewHealthServer(checker)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

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
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())
}

func TestHealthServer_Unhealthy(t *testing.T) {
	checker := &health.Checker{
		Version: "1.0.0",
		Checks: []health.DependencyCheck{
			{
				Name:     "test-db",
				Critical: true,
				Check: func(context.Context) string {
					return health.StatusUnhealthy
				},
			},
		},
	}

	hs := grpcx.NewHealthServer(checker)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

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
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})

	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, resp.GetStatus())
}

func TestHealthServer_NamedServiceReturnsNotFound(t *testing.T) {
	checker := &health.Checker{Version: "1.0.0"}
	hs := grpcx.NewHealthServer(checker)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

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
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{
		Service: "some-service",
	})

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestHealthServer_WatchReturnsUnimplemented(t *testing.T) {
	checker := &health.Checker{Version: "1.0.0"}
	hs := grpcx.NewHealthServer(checker)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, hs)

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
	// Watch starts without error; the error comes from Recv.
	if err == nil {
		_, err = stream.Recv()
	}

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code())
}

func TestNewHealthServer_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.NewHealthServer(nil)
	})
}
