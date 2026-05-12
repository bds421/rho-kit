package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/bds421/rho-kit/grpcx/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// grpcModule implements the Module interface for gRPC server lifecycle.
// It creates a grpc.Server with the provided options, calls the registrar
// to register service implementations, and manages graceful shutdown.
type grpcModule struct {
	BaseModule

	registrar func(*grpc.Server)
	addr      string
	opts      []grpcx.ServerOption
	tlsConfig *tls.Config // injected by Builder.Run when the kit's serverTLS is non-nil

	// initialized during Init
	server *grpc.Server
	logger *slog.Logger

	// stopOnce makes Stop idempotent so the Runner (when the module is also
	// registered as a [lifecycle.Component]) and the module cleanup loop can
	// safely call Stop in either order without a double GracefulStop race.
	stopOnce sync.Once
	stopErr  error
}

// setTLSConfig is called by [Builder.Run] when the kit-level TLS
// configuration is active. The module prepends the credentials option
// onto the caller-supplied opts so the gRPC server inherits the same
// TLS surface as the HTTP server — services that set TLS_CERT/TLS_KEY
// don't silently run plaintext gRPC.
func (m *grpcModule) setTLSConfig(cfg *tls.Config) {
	m.tlsConfig = cfg
}

// NewGRPCModule creates a module that runs a gRPC server.
// The registrar function is called during Init to register gRPC services.
// Options are passed to grpcx.NewServer.
//
// Panics if registrar is nil or addr is empty (startup-time configuration errors).
func NewGRPCModule(registrar func(*grpc.Server), addr string, opts ...grpcx.ServerOption) Module {
	return newGRPCModule(registrar, addr, opts)
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
		opts:       append([]grpcx.ServerOption(nil), opts...),
	}
}

func (m *grpcModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	opts := m.opts
	if m.tlsConfig != nil {
		// Prepend the credentials option so caller overrides still win
		// (the last grpc.Creds applied is what gRPC uses).
		creds := credentials.NewTLS(m.tlsConfig)
		opts = append([]grpcx.ServerOption{grpcx.WithGRPCServerOptions(grpc.Creds(creds))}, m.opts...)
		mc.Logger.Info("gRPC server TLS auto-wired from kit serverTLS")
	}

	m.server = grpcx.NewServer(opts...)
	m.registrar(m.server)

	mc.Logger.Info("gRPC server configured", slog.String("addr", m.addr))
	return nil
}

// RegisterHealth registers the gRPC health service on the public gRPC server
// using the provided health checker. Builder calls this only when
// WithPublicGRPCHealth is set; by default, gRPC health is served from the
// internal ops listener instead.
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

// Start implements lifecycle.Component for the gRPC server.
func (m *grpcModule) Start(_ context.Context) error {
	return m.serve()
}

// Stop implements both [Module.Stop] and [lifecycle.Component.Stop]. It is
// idempotent: the first call attempts a graceful shutdown until ctx expires
// (falling back to a hard stop); subsequent calls return the same result
// without invoking GracefulStop again. This makes Stop safe to call from the
// Runner and the module cleanup loop in either order.
func (m *grpcModule) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.stopOnce.Do(func() {
		m.stopErr = m.gracefulStop(ctx)
	})
	return m.stopErr
}

// gracefulStop attempts a graceful shutdown of the gRPC server. If ctx expires
// before the graceful stop completes, it falls back to a hard stop and returns
// ctx.Err().
func (m *grpcModule) gracefulStop(ctx context.Context) error {
	if m == nil || m.server == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("app: gRPC graceful stop requires a non-nil context")
	}
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}

	done := make(chan struct{})
	go func() {
		m.server.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("gRPC server stopped gracefully")
		return nil
	case <-ctx.Done():
		logger.Warn("gRPC graceful stop context expired, forcing stop", slog.Any("error", ctx.Err()))
		m.server.Stop()
		<-done
		return ctx.Err()
	}
}

// serve starts the gRPC server on the configured address.
func (m *grpcModule) serve() error {
	if m == nil || m.server == nil {
		return errors.New("app: gRPC module is not initialized")
	}
	lis, err := net.Listen("tcp", m.addr)
	if err != nil {
		return fmt.Errorf("gRPC listen failed")
	}

	m.logger.Info("gRPC server listening", slog.String("addr", m.addr))
	if err := m.server.Serve(lis); err != nil {
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return fmt.Errorf("gRPC server error: %w", err)
	}
	return nil
}
