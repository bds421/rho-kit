package grpcx_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/bds421/rho-kit/grpcx"
)

func TestNewServer_DefaultsDoNotPanic(t *testing.T) {
	srv := grpcx.NewServer()
	require.NotNil(t, srv)
	srv.Stop()
}

func TestNewServer_WithOptions(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithMaxRecvMsgSize(8 << 20),
		grpcx.WithMaxSendMsgSize(8 << 20),
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
