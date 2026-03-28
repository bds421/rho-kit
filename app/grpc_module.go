package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/bds421/rho-kit/grpcx"
	"github.com/bds421/rho-kit/observability/health"
)

// grpcModule implements the Module interface for gRPC server lifecycle.
// It creates a grpc.Server with the provided options, calls the registrar
// to register service implementations, registers the gRPC health service,
// and manages graceful shutdown.
type grpcModule struct {
	BaseModule

	registrar func(*grpc.Server)
	addr      string
	opts      []grpcx.ServerOption

	// initialized during Init
	server *grpc.Server
	logger *slog.Logger
}

// newGRPCModule creates a gRPC module with the given registrar, address, and options.
// Panics if registrar is nil (startup-time configuration error).
func newGRPCModule(registrar func(*grpc.Server), addr string, opts []grpcx.ServerOption) *grpcModule {
	if registrar == nil {
		panic("app: gRPC registrar must not be nil")
	}
	if addr == "" {
		panic("app: gRPC address must not be empty")
	}
	return &grpcModule{
		BaseModule: NewBaseModule("grpc"),
		registrar:  registrar,
		addr:       addr,
		opts:       opts,
	}
}

func (m *grpcModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	m.server = grpcx.NewServer(m.opts...)
	m.registrar(m.server)

	mc.Logger.Info("gRPC server configured", "addr", m.addr)
	return nil
}

// RegisterHealth registers the gRPC health service on the server using the
// provided health checker. This is called after all modules are initialized
// so the checker includes all dependency checks.
func (m *grpcModule) RegisterHealth(checker *health.Checker) {
	if m.server == nil || checker == nil {
		return
	}
	healthpb.RegisterHealthServer(m.server, grpcx.NewHealthServer(checker))
}

func (m *grpcModule) HealthChecks() []health.DependencyCheck {
	return nil
}

func (m *grpcModule) Populate(infra *Infrastructure) {
	infra.GRPCServer = m.server
}

// Close is a no-op because the gRPC server lifecycle is managed by the serve
// method, which responds to context cancellation by triggering graceful stop.
// This avoids a double GracefulStop race between the runner stopping the
// component and module cleanup calling Close.
func (m *grpcModule) Close(_ context.Context) error {
	return nil
}

// gracefulStop attempts a graceful shutdown of the gRPC server with a timeout.
// If the graceful stop does not complete within the timeout, it falls back to
// a hard stop.
func (m *grpcModule) gracefulStop() error {
	if m.server == nil {
		return nil
	}

	done := make(chan struct{})
	go func() {
		m.server.GracefulStop()
		close(done)
	}()

	const shutdownTimeout = 10 * time.Second
	select {
	case <-done:
		m.logger.Info("gRPC server stopped gracefully")
	case <-time.After(shutdownTimeout):
		m.logger.Warn("gRPC graceful stop timed out, forcing stop")
		m.server.Stop()
	}
	return nil
}

// serve starts the gRPC server on the configured address. When the context
// is cancelled (shutdown signal), it triggers a graceful stop with a timeout
// fallback to a hard stop.
func (m *grpcModule) serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("gRPC listen on %s: %w", m.addr, err)
	}

	// When the runner cancels the context, trigger graceful stop.
	go func() {
		<-ctx.Done()
		_ = m.gracefulStop()
	}()

	m.logger.Info("gRPC server listening", "addr", m.addr)
	if err := m.server.Serve(lis); err != nil {
		return fmt.Errorf("gRPC server error: %w", err)
	}
	return nil
}
