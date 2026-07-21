package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/grpcx/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

// ResourceKey is the Infrastructure.Resource key under which the Module
// publishes its [*grpc.Server]. Use [Server] to retrieve the typed handle.
const ResourceKey = "github.com/bds421/rho-kit/app/grpc"

// Option configures the gRPC [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	opts         []grpcx.ServerOption
	publicHealth bool
}

// WithServerOption appends a [grpcx.ServerOption] to the underlying server
// builder. Server options stack — pass multiple times to configure
// interceptors, keepalive, message size limits.
//
// The variadic slice is defensively copied so callers cannot mutate the
// captured options after construction (matches the canonical Option
// shape used elsewhere in the kit — see app/redis.WithConn).
func WithServerOption(opts ...grpcx.ServerOption) Option {
	captured := append([]grpcx.ServerOption(nil), opts...)
	return func(c *moduleConfig) {
		c.opts = append(c.opts, captured...)
	}
}

// WithPublicHealth registers the gRPC Health Checking Protocol on the public
// gRPC listener (default: off). The internal ops listener always exposes
// gRPC health over h2c regardless.
//
// Use this opt-in only when the public gRPC listener is protected by network
// policy or the health service is intentionally part of the public contract.
func WithPublicHealth() Option {
	return func(c *moduleConfig) {
		c.publicHealth = true
	}
}

// Module returns an [app.Module] that runs a gRPC server on addr and
// registers services via registrar. Pass to [app.Builder.With].
//
// Rate limiting: Builder.Validate's rate-limit declaration (ratelimit.IP)
// covers the public HTTP listener only. This gRPC listener is a separate
// public surface — grpcx stream caps bound concurrency per connection but
// do not throttle request rate or connection acceptance. Operators must
// add interceptors (or an external gateway limit) for gRPC; registering
// ratelimit.IP alone does NOT throttle this port.
//
// Panics if registrar is nil or addr is empty (startup-time configuration
// errors).
func Module(registrar func(*grpc.Server), addr string, opts ...Option) app.Module {
	if registrar == nil {
		panic("grpc: Module requires a non-nil registrar")
	}
	if addr == "" {
		panic("grpc: Module requires a non-empty address")
	}
	mc := moduleConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("grpc: Module option must not be nil")
		}
		opt(&mc)
	}
	return &grpcModule{
		registrar:    registrar,
		addr:         addr,
		opts:         append([]grpcx.ServerOption(nil), mc.opts...),
		publicHealth: mc.publicHealth,
	}
}

// grpcModule implements the Module interface for gRPC server lifecycle.
type grpcModule struct {
	app.BaseModule

	registrar    func(*grpc.Server)
	addr         string
	opts         []grpcx.ServerOption
	publicHealth bool
	tlsConfig    *tls.Config // injected by Builder.Run when the kit's serverTLS is non-nil

	// initialized during Init
	server  *grpc.Server
	logger  *slog.Logger
	checker *health.Checker // set by Builder.Run via SetHealthChecker before public-health registration

	// stopOnce makes Stop idempotent so the Runner and the module cleanup
	// loop can safely call Stop in either order without a double GracefulStop
	// race.
	stopOnce sync.Once
	stopErr  error
}

// ModuleName is the registered Module.Name() value.
const ModuleName = "grpc"

func (m *grpcModule) Name() string { return ModuleName }

// SetServerTLS implements [app.ServerTLSReceiver]. The Builder hands the
// resolved kit-level *tls.Config to this hook before Init runs so the
// gRPC server can be constructed with matching credentials.
func (m *grpcModule) SetServerTLS(cfg *tls.Config) {
	m.tlsConfig = cfg
}

// SetHealthChecker implements [app.HealthCheckerReceiver]. The Builder hands
// the resolved [*health.Checker] to this hook so the module can register the
// gRPC Health Checking Protocol on its grpc.Server when [WithPublicHealth]
// was passed.
func (m *grpcModule) SetHealthChecker(checker *health.Checker) {
	m.checker = checker
	if m.publicHealth {
		m.registerPublicHealth()
	}
}

// AttachToRunner implements [app.RunnerAttacher]. The Builder calls this
// after Init so the gRPC server participates in the lifecycle Runner
// (stopped AFTER the public HTTP server in reverse registration order).
func (m *grpcModule) AttachToRunner(runner *lifecycle.Runner) {
	runner.Add("grpc-server", m)
}

// WrapInternalHandler implements [app.InternalHandlerWrapper]. When the
// gRPC adapter is registered, the kit's internal-ops port additionally
// serves the gRPC Health Checking Protocol over h2c on the same listener
// as HTTP /ready, so internal callers can probe either protocol.
func (m *grpcModule) WrapInternalHandler(base http.Handler, checker *health.Checker) http.Handler {
	return withInternalGRPCHealth(base, checker)
}

// ConfigureInternalServer implements [app.InternalServerConfigurator]. The
// gRPC health service rides the kit's internal-ops listener as cleartext
// HTTP/2 (h2c) so internal probes can dial either HTTP /ready or the gRPC
// Health.Check RPC on the same port. Go 1.24 introduced http.Server.Protocols
// as the supported way to opt into unencrypted HTTP/2; this replaces the
// previous h2c.NewHandler wrapper (deprecated in Go 1.26).
func (m *grpcModule) ConfigureInternalServer(srv *http.Server) {
	if srv == nil {
		return
	}
	protocols := srv.Protocols
	if protocols == nil {
		protocols = &http.Protocols{}
	}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv.Protocols = protocols
}

func (m *grpcModule) Init(_ context.Context, mc app.ModuleContext) error {
	m.logger = mc.Logger

	opts := m.opts
	if m.tlsConfig != nil {
		// Prepend the credentials option so caller overrides still win
		// (the last grpc.Creds applied is what gRPC uses).
		creds := credentials.NewTLS(m.tlsConfig)
		opts = append([]grpcx.ServerOption{grpcx.WithGRPCServerOptions(grpc.Creds(creds))}, m.opts...)
		mc.Logger.Info("gRPC server TLS auto-wired from kit serverTLS")
	} else {
		// Public gRPC without kit serverTLS is plaintext. Call this out
		// loudly - http.WithoutTLS only documents the HTTP surface, and
		// credentials in metadata would otherwise cross the network clear.
		mc.Logger.Warn("gRPC server starting without transport credentials (plaintext); set TLS_CERT/TLS_KEY or supply grpc.Creds via WithGRPCServerOptions")
	}

	m.server = grpcx.NewServer(opts...)
	m.registrar(m.server)

	mc.Logger.Info("gRPC server configured", slog.String("addr", m.addr))
	return nil
}

func (m *grpcModule) registerPublicHealth() {
	if m.server == nil || m.checker == nil {
		return
	}
	healthpb.RegisterHealthServer(m.server, grpcx.NewHealthServer(m.checker))
}

// RegisterHealth registers the gRPC health service on the public gRPC server
// using the provided health checker. Exposed for tests that drive the module
// directly. Production wiring goes through SetHealthChecker + WithPublicHealth.
func (m *grpcModule) RegisterHealth(checker *health.Checker) {
	if m.server == nil || checker == nil {
		return
	}
	healthpb.RegisterHealthServer(m.server, grpcx.NewHealthServer(checker))
}

func (m *grpcModule) HealthChecks() []health.DependencyCheck {
	return nil
}

func (m *grpcModule) Populate(infra *app.Infrastructure) {
	if m.server == nil {
		return
	}
	infra.SetResource(ResourceKey, m.server)
}

// Start implements lifecycle.Component for the gRPC server.
func (m *grpcModule) Start(_ context.Context) error {
	return m.serve()
}

// Stop implements both [app.Module.Stop] and [lifecycle.Component.Stop]. It
// is idempotent.
func (m *grpcModule) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.stopOnce.Do(func() {
		m.stopErr = m.gracefulStop(ctx)
	})
	return m.stopErr
}

func (m *grpcModule) gracefulStop(ctx context.Context) error {
	if m == nil || m.server == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("grpc: gracefulStop requires a non-nil context")
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
		logger.Warn("gRPC graceful stop context expired, forcing stop", redact.Error(ctx.Err()))
		m.server.Stop()
		<-done
		return ctx.Err()
	}
}

func (m *grpcModule) serve() error {
	if m == nil || m.server == nil {
		return errors.New("grpc: module is not initialized")
	}
	lis, err := net.Listen("tcp", m.addr)
	if err != nil {
		// Log the redacted cause (concrete error type + chain) so an
		// operator can distinguish a port conflict (EADDRINUSE) from a
		// permissions or invalid-address failure. The returned error stays
		// sanitized so the listen address is never leaked upstream.
		m.logger.Error("gRPC listen failed", redact.Error(err), redact.ErrorChain(err))
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

// withInternalGRPCHealth layers the gRPC health-checking protocol over h2c
// onto the kit's internal-ops listener so callers may probe via either
// HTTP /ready or the gRPC Health.Check RPC.
//
// The h2c surface is opted-in on the *http.Server itself via
// [grpcModule.ConfigureInternalServer] (http.Server.Protocols replaced the
// deprecated h2c.NewHandler wrapper in Go 1.24+). This function returns a
// plain handler that dispatches gRPC requests (recognized by the
// application/grpc Content-Type) to the gRPC mux and everything else to
// the base /ready handler.
func withInternalGRPCHealth(base http.Handler, checker *health.Checker) http.Handler {
	if base == nil {
		panic("grpc: internal handler must not be nil")
	}
	if checker == nil {
		return base
	}

	grpcHealth := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcHealth, grpcx.NewHealthServer(checker))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internalGRPCHealthRequest(r) {
			grpcHealth.ServeHTTP(w, r)
			return
		}
		base.ServeHTTP(w, r)
	})
}

func internalGRPCHealthRequest(r *http.Request) bool {
	if r == nil || r.ProtoMajor != 2 {
		return false
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	contentType := values[0]
	if contentType == "" || !utf8.ValidString(contentType) || !httpguts.ValidHeaderFieldValue(contentType) {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "application/grpc" {
		return true
	}
	if !strings.HasPrefix(contentType, "application/grpc") {
		return false
	}
	switch contentType[len("application/grpc")] {
	case '+', ';':
		return true
	default:
		return false
	}
}

// Server returns the public gRPC server published by [Module] under
// [ResourceKey], or nil if no grpc adapter was registered.
func Server(infra app.Infrastructure) *grpc.Server {
	v, ok := infra.Resource(ResourceKey)
	if !ok {
		return nil
	}
	s, _ := v.(*grpc.Server)
	return s
}
