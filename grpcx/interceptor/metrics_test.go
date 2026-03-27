package interceptor_test

import (
	"context"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/bds421/rho-kit/grpcx/interceptor"
)

func TestGRPCMetrics_RecordsRequestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := interceptor.NewGRPCMetrics(reg)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(metrics.UnaryInterceptor()),
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

	// Verify counter was recorded.
	families, err := reg.Gather()
	require.NoError(t, err)

	var foundCounter, foundHistogram bool
	for _, fam := range families {
		switch fam.GetName() {
		case "grpc_server_handled_total":
			foundCounter = true
			require.NotEmpty(t, fam.GetMetric())
			m := fam.GetMetric()[0]
			assert.Equal(t, float64(1), m.GetCounter().GetValue())
		case "grpc_server_handling_seconds":
			foundHistogram = true
			require.NotEmpty(t, fam.GetMetric())
			m := fam.GetMetric()[0]
			assert.Equal(t, uint64(1), m.GetHistogram().GetSampleCount())
		}
	}
	assert.True(t, foundCounter, "grpc_server_handled_total metric not found")
	assert.True(t, foundHistogram, "grpc_server_handling_seconds metric not found")
}

func TestGRPCMetrics_DefaultRegisterer(t *testing.T) {
	// Should not panic with nil registerer.
	m := interceptor.NewGRPCMetrics(nil)
	assert.NotNil(t, m)
}

func TestGRPCMetrics_DuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := interceptor.NewGRPCMetrics(reg)
	m2 := interceptor.NewGRPCMetrics(reg)
	assert.NotNil(t, m1)
	assert.NotNil(t, m2)
}

func TestGRPCMetrics_StreamInterceptor_RecordsMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := interceptor.NewGRPCMetrics(reg)

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(
		grpc.ChainStreamInterceptor(metrics.StreamInterceptor()),
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

	families, err := reg.Gather()
	require.NoError(t, err)

	var foundCounter bool
	for _, fam := range families {
		if fam.GetName() == "grpc_server_handled_total" {
			foundCounter = true
			require.NotEmpty(t, fam.GetMetric())
			m := fam.GetMetric()[0]
			assert.Equal(t, float64(1), m.GetCounter().GetValue())
		}
	}
	assert.True(t, foundCounter, "grpc_server_handled_total metric not found for stream RPC")
}
