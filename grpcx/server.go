package grpcx

import (
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/bds421/rho-kit/grpcx/interceptor"
)

// ServerOption configures the gRPC server returned by NewServer.
type ServerOption func(*serverConfig)

type serverConfig struct {
	unaryInterceptors  []grpc.UnaryServerInterceptor
	streamInterceptors []grpc.StreamServerInterceptor
	grpcOpts           []grpc.ServerOption
	maxRecvMsgSize     int
	maxSendMsgSize     int
	keepaliveParams    *keepalive.ServerParameters
	keepalivePolicy    *keepalive.EnforcementPolicy
	disableRecovery    bool
	recoveryLogger     *slog.Logger
}

const (
	// defaultMaxRecvMsgSize is 4 MB, matching the gRPC default.
	defaultMaxRecvMsgSize = 4 << 20

	// defaultMaxSendMsgSize is 4 MB, matching the gRPC default.
	defaultMaxSendMsgSize = 4 << 20
)

// defaultKeepalive returns production-safe keepalive parameters.
func defaultKeepalive() keepalive.ServerParameters {
	return keepalive.ServerParameters{
		MaxConnectionIdle:     5 * time.Minute,
		MaxConnectionAge:      30 * time.Minute,
		MaxConnectionAgeGrace: 10 * time.Second,
		Time:                  2 * time.Minute,
		Timeout:               20 * time.Second,
	}
}

// defaultEnforcementPolicy returns a keepalive enforcement policy that prevents
// misbehaving clients from sending pings too frequently.
func defaultEnforcementPolicy() keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime:             30 * time.Second,
		PermitWithoutStream: true,
	}
}

// WithUnaryInterceptors appends unary server interceptors.
// Interceptors are chained in the order provided.
func WithUnaryInterceptors(interceptors ...grpc.UnaryServerInterceptor) ServerOption {
	return func(c *serverConfig) {
		c.unaryInterceptors = append(c.unaryInterceptors, interceptors...)
	}
}

// WithStreamInterceptors appends stream server interceptors.
func WithStreamInterceptors(interceptors ...grpc.StreamServerInterceptor) ServerOption {
	return func(c *serverConfig) {
		c.streamInterceptors = append(c.streamInterceptors, interceptors...)
	}
}

// WithMaxRecvMsgSize sets the maximum message size the server can receive.
// Panics if size is not positive to fail fast on misconfiguration.
func WithMaxRecvMsgSize(size int) ServerOption {
	if size <= 0 {
		panic("grpcx: WithMaxRecvMsgSize requires a positive size")
	}
	return func(c *serverConfig) { c.maxRecvMsgSize = size }
}

// WithMaxSendMsgSize sets the maximum message size the server can send.
// Panics if size is not positive to fail fast on misconfiguration.
func WithMaxSendMsgSize(size int) ServerOption {
	if size <= 0 {
		panic("grpcx: WithMaxSendMsgSize requires a positive size")
	}
	return func(c *serverConfig) { c.maxSendMsgSize = size }
}

// WithKeepaliveParams overrides the default keepalive parameters.
func WithKeepaliveParams(params keepalive.ServerParameters) ServerOption {
	return func(c *serverConfig) { c.keepaliveParams = &params }
}

// WithKeepalivePolicy overrides the default keepalive enforcement policy.
func WithKeepalivePolicy(policy keepalive.EnforcementPolicy) ServerOption {
	return func(c *serverConfig) { c.keepalivePolicy = &policy }
}

// WithGRPCServerOptions appends raw grpc.ServerOption values for cases not
// covered by the typed options above.
func WithGRPCServerOptions(opts ...grpc.ServerOption) ServerOption {
	return func(c *serverConfig) {
		c.grpcOpts = append(c.grpcOpts, opts...)
	}
}

// WithoutRecovery disables the panic-recovery interceptors that NewServer
// installs by default. Strongly discouraged in production: a handler panic
// will tear down the gRPC connection without a structured log entry. Use
// only for tests that intentionally observe panic propagation.
func WithoutRecovery() ServerOption {
	return func(c *serverConfig) { c.disableRecovery = true }
}

// WithRecoveryLogger overrides the logger passed to the recovery
// interceptors. Defaults to slog.Default().
func WithRecoveryLogger(l *slog.Logger) ServerOption {
	return func(c *serverConfig) { c.recoveryLogger = l }
}

// NewServer returns a *grpc.Server with production defaults: keepalive,
// message size limits, panic-recovery interceptors, and the user-supplied
// interceptors.
//
// Recovery interceptors are PREPENDED so they wrap every other interceptor
// and the handler itself: a panic anywhere in the chain converts to
// codes.Internal with a structured log entry rather than tearing down the
// connection silently. Disable with [WithoutRecovery] only when tests need
// to assert raw panic propagation.
//
// Options are applied in order; later options override earlier ones.
func NewServer(opts ...ServerOption) *grpc.Server {
	cfg := serverConfig{
		maxRecvMsgSize: defaultMaxRecvMsgSize,
		maxSendMsgSize: defaultMaxSendMsgSize,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	kp := defaultKeepalive()
	if cfg.keepaliveParams != nil {
		kp = *cfg.keepaliveParams
	}

	ep := defaultEnforcementPolicy()
	if cfg.keepalivePolicy != nil {
		ep = *cfg.keepalivePolicy
	}

	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.maxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.maxSendMsgSize),
		grpc.KeepaliveParams(kp),
		grpc.KeepaliveEnforcementPolicy(ep),
	}

	unary := cfg.unaryInterceptors
	stream := cfg.streamInterceptors
	if !cfg.disableRecovery {
		recLogger := cfg.recoveryLogger
		if recLogger == nil {
			recLogger = slog.Default()
		}
		unary = append([]grpc.UnaryServerInterceptor{interceptor.RecoveryUnary(recLogger)}, unary...)
		stream = append([]grpc.StreamServerInterceptor{interceptor.RecoveryStream(recLogger)}, stream...)
	}

	if len(unary) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainStreamInterceptor(stream...))
	}

	grpcOpts = append(grpcOpts, cfg.grpcOpts...)

	return grpc.NewServer(grpcOpts...)
}
