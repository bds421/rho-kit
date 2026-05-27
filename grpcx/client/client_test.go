package client_test

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	grpcx "github.com/bds421/rho-kit/grpcx/v2"
	"github.com/bds421/rho-kit/grpcx/v2/client"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

func startTestServer(t *testing.T) (string, *grpc.Server) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpcx.NewServer(
		grpcx.WithoutLogging(),
		grpcx.WithoutMetrics(),
	)
	// Register the standard health service so we have something callable.
	healthpb.RegisterHealthServer(srv, &fakeHealthSrv{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String(), srv
}

type fakeHealthSrv struct{ healthpb.UnimplementedHealthServer }

func (f *fakeHealthSrv) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestNewClient_LoopbackInsecureDials(t *testing.T) {
	addr, _ := startTestServer(t)

	conn, err := client.NewClient(addr,
		client.WithInsecure(),
		client.WithoutLogging(),
		client.WithoutMetrics(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := healthpb.NewHealthClient(conn)
	resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("Status = %v, want SERVING", resp.Status)
	}
}

func TestNewClient_PanicsOnInsecureNonLoopback(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on insecure non-loopback")
		}
	}()
	_, _ = client.NewClient("8.8.8.8:443", client.WithInsecure())
}

func TestNewClient_PanicsWithoutCredentialsOrInsecure(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when neither TLS nor insecure passed")
		}
	}()
	_, _ = client.NewClient("example.com:443")
}

func TestNewClient_PanicsOnEmptyTarget(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on empty target")
		}
	}()
	_, _ = client.NewClient("")
}

func TestNewClient_PanicsOnNilTLSConfig(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil tls.Config")
		}
	}()
	_ = client.WithTLSConfig(nil)
}

func TestNewClient_RejectsInsecureSkipVerifyTLSConfig(t *testing.T) {
	// The TLS floor helper (tlsclone.ConfigWithFloor) rejects any
	// caller-supplied *tls.Config that sets InsecureSkipVerify=true.
	// Verify NewClient surfaces that error instead of silently dialing
	// an unverified peer.
	_, err := client.NewClient("api.example.com:443",
		client.WithTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
		}),
		client.WithoutLogging(),
		client.WithoutMetrics(),
	)
	require.Error(t, err, "InsecureSkipVerify=true must be rejected; NewClient returned no error")
	require.Contains(t, err.Error(), "InsecureSkipVerify",
		"expected the rejection to mention InsecureSkipVerify, got %v", err)
}

func TestNewClient_AcceptsTLSConfigWithFloor(t *testing.T) {
	// We're not actually dialing — just verifying NewClient accepts a
	// caller-supplied tls.Config without panicking and floors MinVersion.
	conn, err := client.NewClient("127.0.0.1:1",
		client.WithTLSConfig(&tls.Config{MinVersion: tls.VersionTLS13}),
		client.WithoutLogging(),
		client.WithoutMetrics(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = conn.Close()
}

func TestNewClient_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil option")
		}
	}()
	_, _ = client.NewClient("127.0.0.1:1", nil)
}

// startFlakyServer registers a health service that fails the first
// failures-many calls with Unavailable, then returns SERVING.
func startFlakyServer(t *testing.T, failures int) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpcx.NewServer(grpcx.WithoutLogging(), grpcx.WithoutMetrics())
	healthpb.RegisterHealthServer(srv, &flakyHealthSrv{failuresLeft: failures})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}

type flakyHealthSrv struct {
	healthpb.UnimplementedHealthServer
	mu           sync.Mutex
	failuresLeft int
}

func (f *flakyHealthSrv) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return nil, status.Error(codes.Unavailable, "transient")
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestNewClient_RetryRetriesUnavailable(t *testing.T) {
	addr := startFlakyServer(t, 2)
	conn, err := client.NewClient(addr,
		client.WithInsecure(),
		client.WithRetry(retry.DefaultPolicy()),
		client.WithoutLogging(),
		client.WithoutMetrics(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := healthpb.NewHealthClient(conn)
	resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check after retry: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("Status = %v after retry, want SERVING", resp.Status)
	}
}

func TestNewClient_MetricsRegistererPanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil registerer")
		}
	}()
	_ = client.WithMetricsRegisterer(nil)
}

func TestNewClient_MetricsRegistererIsolation(t *testing.T) {
	reg := prometheus.NewRegistry()
	addr, _ := startTestServer(t)

	conn, err := client.NewClient(addr,
		client.WithInsecure(),
		client.WithMetricsRegisterer(reg),
		client.WithoutLogging(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := healthpb.NewHealthClient(conn)
	_, _ = c.Check(ctx, &healthpb.HealthCheckRequest{})

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() == "grpc_client_handled_total" {
			found = true
		}
	}
	if !found {
		t.Fatalf("grpc_client_handled_total not registered on custom registry")
	}
}
