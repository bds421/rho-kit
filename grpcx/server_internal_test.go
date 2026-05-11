package grpcx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestWithUnaryInterceptorsClonesInput(t *testing.T) {
	var calls []string
	interceptors := []grpc.UnaryServerInterceptor{
		func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			calls = append(calls, "original")
			return handler(ctx, req)
		},
	}
	opt := WithUnaryInterceptors(interceptors...)
	interceptors[0] = func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		calls = append(calls, "mutated")
		return handler(ctx, req)
	}

	var cfg serverConfig
	opt(&cfg)
	resp, err := cfg.unaryInterceptors[0](context.Background(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return "ok", nil
	})

	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.Equal(t, []string{"original"}, calls)
}

func TestWithStreamInterceptorsClonesInput(t *testing.T) {
	var calls []string
	interceptors := []grpc.StreamServerInterceptor{
		func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			calls = append(calls, "original")
			return handler(srv, ss)
		},
	}
	opt := WithStreamInterceptors(interceptors...)
	interceptors[0] = func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		calls = append(calls, "mutated")
		return handler(srv, ss)
	}

	var cfg serverConfig
	opt(&cfg)
	err := cfg.streamInterceptors[0](nil, nil, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error {
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, []string{"original"}, calls)
}
