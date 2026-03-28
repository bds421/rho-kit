package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/runtime/lifecycle"
)

func TestNewGRPCModule_ReturnsModule(t *testing.T) {
	m := NewGRPCModule(func(_ *grpc.Server) {}, ":50051")
	assert.Equal(t, "grpc", m.Name())
}

func TestNewGRPCModule_PanicsOnNilRegistrarViaExported(t *testing.T) {
	assert.Panics(t, func() {
		NewGRPCModule(nil, ":50051")
	})
}

func TestNewGRPCModule_PanicsOnEmptyAddrViaExported(t *testing.T) {
	assert.Panics(t, func() {
		NewGRPCModule(func(_ *grpc.Server) {}, "")
	})
}

func TestNewGRPCModule_WithModuleChaining(t *testing.T) {
	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithModule(NewGRPCModule(func(_ *grpc.Server) {}, ":50051"))
	assert.NotNil(t, b)
}

func TestNewGRPCModule_PanicsOnNilRegistrar(t *testing.T) {
	assert.Panics(t, func() {
		newGRPCModule(nil, ":50051", nil)
	})
}

func TestNewGRPCModule_PanicsOnEmptyAddr(t *testing.T) {
	assert.Panics(t, func() {
		newGRPCModule(func(_ *grpc.Server) {}, "", nil)
	})
}

func TestGRPCModule_Name(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)
	assert.Equal(t, "grpc", m.Name())
}

func TestGRPCModule_InitCallsRegistrar(t *testing.T) {
	var registrarCalled atomic.Bool

	m := newGRPCModule(func(_ *grpc.Server) {
		registrarCalled.Store(true)
	}, ":50051", nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
		Config: BaseConfig{},
	}

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)

	assert.True(t, registrarCalled.Load(), "registrar should have been called")
	assert.NotNil(t, m.server, "server should be initialized")
}

func TestGRPCModule_PopulateSetsGRPCServer(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
		Config: BaseConfig{},
	}

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)

	infra := &Infrastructure{}
	m.Populate(infra)
	assert.NotNil(t, infra.GRPCServer, "GRPCServer should be set on Infrastructure")
	assert.Equal(t, m.server, infra.GRPCServer)
}

func TestGRPCModule_HealthChecksEmpty(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)
	assert.Nil(t, m.HealthChecks())
}

func TestGRPCModule_RegisterHealthWiresService(t *testing.T) {
	grpcPort := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	m := newGRPCModule(func(_ *grpc.Server) {}, addr, nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
		Config: BaseConfig{},
	}

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)

	checker := &health.Checker{
		Version: "test",
		Checks: []health.DependencyCheck{
			{
				Name:     "test-dep",
				Check:    func(_ context.Context) string { return health.StatusHealthy },
				Critical: true,
			},
		},
	}
	m.RegisterHealth(checker)

	// Start serving in background with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- m.serve(ctx) }()

	// Wait for server to be ready.
	waitForGRPC(t, addr, 3*time.Second)

	// Check health.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())

	// Clean up via context cancellation (like the runner does).
	cancel()
	select {
	case srvErr := <-errCh:
		// Serve returns nil after GracefulStop.
		assert.NoError(t, srvErr)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop within timeout")
	}
}

func TestGRPCModule_CloseBeforeInit(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)
	// Close before Init should not panic or error.
	assert.NoError(t, m.Close(context.Background()))
}

func TestGRPCModule_RegisterHealthNilChecker(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
		Config: BaseConfig{},
	}

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)

	// Should not panic with nil checker.
	m.RegisterHealth(nil)
}

func TestGRPCModule_RegisterHealthBeforeInit(t *testing.T) {
	m := newGRPCModule(func(_ *grpc.Server) {}, ":50051", nil)
	// Should not panic when server is nil.
	m.RegisterHealth(&health.Checker{Version: "test"})
}

func TestGRPCModule_Lifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	grpcPort := freePort(t)
	srvPort := freePort(t)
	intPort := freePort(t)

	cfg := BaseConfig{
		Server:   ServerConfig{Host: "127.0.0.1", Port: srvPort},
		Internal: InternalConfig{Host: "127.0.0.1", Port: intPort},
	}

	var registrarCalled atomic.Bool

	b := New("grpc-test", "v0.0.1", cfg).
		WithModule(NewGRPCModule(func(_ *grpc.Server) {
			registrarCalled.Store(true)
		}, fmt.Sprintf("127.0.0.1:%d", grpcPort))).
		Router(func(infra Infrastructure) http.Handler {
			assert.NotNil(t, infra.GRPCServer, "GRPCServer should be set")
			infra.Background("force-exit", func(_ context.Context) error {
				return errors.New("intentional shutdown")
			})
			return http.NotFoundHandler()
		})

	err := b.Run()
	// The intentional shutdown error is expected.
	assert.Error(t, err)
	assert.True(t, registrarCalled.Load(), "gRPC registrar should have been called")
}

// waitForGRPC polls the gRPC address until it accepts connections.
func waitForGRPC(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gRPC server at %s not ready within %v", addr, timeout)
}
