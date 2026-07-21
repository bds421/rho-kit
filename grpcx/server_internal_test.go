package grpcx

import (
	"context"
	"testing"
	"time"

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

func TestWithMaxConcurrentStreamsConfigures(t *testing.T) {
	var cfg serverConfig
	WithMaxConcurrentStreams(7)(&cfg)
	require.Equal(t, uint32(7), cfg.maxConcurrentStreams,
		"WithMaxConcurrentStreams must write through to the config field")
}

func TestDefaultMaxConcurrentStreamsPinned(t *testing.T) {
	// The default must be a small, finite number — the gRPC framework
	// default of math.MaxUint32 is the GAP-03 streaming-flood vector.
	require.Equal(t, uint32(1000), defaultMaxConcurrentStreams,
		"defaultMaxConcurrentStreams must stay pinned at 1000 — a regression to math.MaxUint32 reopens GAP-03")
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

func TestDefaultEnforcementMinTimeBelowClientKeepalive(t *testing.T) {
	// Pin the server MinTime default strictly below the paired client
	// keepalive Time (30s). Equal clocks + jitter risk GOAWAY too_many_pings
	// when MinTime == client Time.
	ep := defaultEnforcementPolicy()
	require.Equal(t, 20*time.Second, ep.MinTime,
		"server default MinTime must stay at 20s (client default Time is 30s)")
}
