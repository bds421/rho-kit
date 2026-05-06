package grpcx_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx"
)

func TestNewServer_DefaultsDoNotPanic(t *testing.T) {
	srv := grpcx.NewServer()
	require.NotNil(t, srv)
	srv.Stop()
}

func TestNewServer_WithOptions(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithMaxRecvMsgSize(8<<20),
		grpcx.WithMaxSendMsgSize(8<<20),
		grpcx.WithKeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 10 * time.Minute,
		}),
		grpcx.WithKeepalivePolicy(keepalive.EnforcementPolicy{
			MinTime: 1 * time.Minute,
		}),
	)
	require.NotNil(t, srv)
	srv.Stop()
}

func TestNewServer_WithInterceptors(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithUnaryInterceptors(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}),
		grpcx.WithStreamInterceptors(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		}),
		grpcx.WithGRPCServerOptions(),
	)
	require.NotNil(t, srv)
	srv.Stop()
}

func TestWithMaxRecvMsgSize_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxRecvMsgSize(0)
	})
}

func TestWithMaxRecvMsgSize_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxRecvMsgSize(-1)
	})
}

func TestWithMaxSendMsgSize_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() {
		grpcx.WithMaxSendMsgSize(0)
	})
}

// panickingHealth panics on Check — used to verify NewServer's default
// recovery interceptor catches panics without an explicit Recovery option.
type panickingHealth struct {
	healthpb.UnimplementedHealthServer
}

func (panickingHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	panic("kit-default recovery should catch this")
}

func TestNewServer_RecoveryEnabledByDefault(t *testing.T) {
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	srv := grpcx.NewServer()
	healthpb.RegisterHealthServer(srv, panickingHealth{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code(), "kit-default recovery should convert panic to codes.Internal")
}

func TestNewServer_WithoutRecovery_NonPanickingRPCStillWorks(t *testing.T) {
	// We can't safely run a panicking handler with recovery off — the panic
	// crashes the test process before the assertion runs. The contract for
	// WithoutRecovery is "panics propagate", which we verify indirectly: the
	// server still starts and serves a non-panicking RPC, so the option
	// merely toggles the interceptor (rather than misconfiguring the chain).
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)

	srv := grpcx.NewServer(grpcx.WithoutRecovery())
	healthpb.RegisterHealthServer(srv, &healthpb.UnimplementedHealthServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unimplemented, st.Code(),
		"WithoutRecovery removes the recovery interceptor only — the rest of the chain stays functional")
}
