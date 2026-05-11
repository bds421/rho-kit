package grpcx

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
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
	defaultDeadline    time.Duration
	disableDefaultDL   bool
	disableLogging     bool
	loggingLogger      *slog.Logger
	disableMetrics     bool
	metricsRegisterer  prometheus.Registerer
}

// DefaultRPCDeadline is the per-RPC deadline applied automatically by
// [NewServer] when the caller does not configure one explicitly. It bounds
// every handler so a streaming RPC or a unary RPC from a crashed client
// cannot pin a goroutine indefinitely. Override with [WithDefaultDeadline]
// or opt out with [WithoutDefaultDeadline].
const DefaultRPCDeadline = 30 * time.Second

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
	for _, interceptor := range interceptors {
		if interceptor == nil {
			panic("grpcx: WithUnaryInterceptors requires non-nil interceptors")
		}
	}
	copied := append([]grpc.UnaryServerInterceptor(nil), interceptors...)
	return func(c *serverConfig) {
		c.unaryInterceptors = append(c.unaryInterceptors, copied...)
	}
}

// WithStreamInterceptors appends stream server interceptors.
func WithStreamInterceptors(interceptors ...grpc.StreamServerInterceptor) ServerOption {
	for _, interceptor := range interceptors {
		if interceptor == nil {
			panic("grpcx: WithStreamInterceptors requires non-nil interceptors")
		}
	}
	copied := append([]grpc.StreamServerInterceptor(nil), interceptors...)
	return func(c *serverConfig) {
		c.streamInterceptors = append(c.streamInterceptors, copied...)
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
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx: WithGRPCServerOptions requires non-nil options")
		}
	}
	copied := append([]grpc.ServerOption(nil), opts...)
	return func(c *serverConfig) {
		c.grpcOpts = append(c.grpcOpts, copied...)
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

// WithDefaultDeadline overrides the [DefaultRPCDeadline] applied by
// [NewServer] for the per-RPC default-deadline interceptor (both unary and
// streaming). The interceptor sets the handler ctx deadline to `now + d`
// when the inbound RPC has no deadline OR has a deadline further out than
// `now + d`. Closer deadlines from the caller are preserved.
//
// Without a server-side cap, a streaming RPC (or a unary RPC from a
// crashed client) can hold a handler open indefinitely. Goroutines
// piling up against a slow downstream cascade to liveness failure
// — exactly the streaming-RPC exhaustion gap GAP-03 in
// docs/audit/THREAT_MODEL.md.
//
// The interceptor is prepended after recovery so panics still convert to
// codes.Internal but every request lands with a bounded ctx.
//
// Panics if d is not positive.
func WithDefaultDeadline(d time.Duration) ServerOption {
	if d <= 0 {
		panic("grpcx: WithDefaultDeadline requires a positive duration")
	}
	return func(c *serverConfig) { c.defaultDeadline = d }
}

// WithoutDefaultDeadline opts the server out of the [DefaultRPCDeadline]
// auto-applied by [NewServer]. Strongly discouraged for production —
// without a server-side cap a slow client can pin a handler goroutine
// forever. Reserved for tests asserting raw cancellation behaviour or
// long-lived bidirectional streams that manage their own lifetimes.
func WithoutDefaultDeadline() ServerOption {
	return func(c *serverConfig) { c.disableDefaultDL = true }
}

// WithLogger overrides the logger passed to the auto-applied logging
// interceptors. Defaults to slog.Default().
func WithLogger(l *slog.Logger) ServerOption {
	return func(c *serverConfig) { c.loggingLogger = l }
}

// WithoutLogging disables the logging interceptors that NewServer installs
// by default. Use only for tests asserting on raw handler invocation.
func WithoutLogging() ServerOption {
	return func(c *serverConfig) { c.disableLogging = true }
}

// WithMetricsRegisterer overrides the Prometheus registerer for the
// auto-applied metrics interceptors. Defaults to
// prometheus.DefaultRegisterer.
func WithMetricsRegisterer(reg prometheus.Registerer) ServerOption {
	return func(c *serverConfig) { c.metricsRegisterer = reg }
}

// WithoutMetrics disables the metrics interceptors that NewServer installs
// by default. Use only when an alternative metrics surface (custom
// interceptor, OTel) is wired manually.
func WithoutMetrics() ServerOption {
	return func(c *serverConfig) { c.disableMetrics = true }
}

// NewServer returns a *grpc.Server with production defaults: keepalive,
// message size limits, recovery + logging + metrics interceptors, a
// per-RPC default deadline, and the user-supplied interceptors.
//
// Final chain order (outermost first):
//
//	recovery -> logging -> metrics -> deadline -> caller-supplied -> handler
//
// Each auto-applied interceptor has a documented opt-out:
//   - [WithoutRecovery]
//   - [WithoutLogging]
//   - [WithoutMetrics]
//   - [WithoutDefaultDeadline]
//
// These exist so tests can assert on raw behaviour but should be avoided
// in production. The defaults exist to close real holes — a panicking
// handler tearing down the connection, an un-bounded streaming RPC
// pinning a goroutine, missing observability — and disabling them
// without a replacement is a regression.
//
// Options are applied in order; later options override earlier ones.
func NewServer(opts ...ServerOption) *grpc.Server {
	cfg := serverConfig{
		maxRecvMsgSize:  defaultMaxRecvMsgSize,
		maxSendMsgSize:  defaultMaxSendMsgSize,
		defaultDeadline: DefaultRPCDeadline,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx: NewServer option must not be nil")
		}
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

	// Inner-to-outer prepends so the final order matches the chain
	// documented above: caller-supplied interceptors stay closest to
	// the handler; deadline wraps them; metrics wraps deadline;
	// logging wraps metrics; recovery is the outermost guard.
	if !cfg.disableDefaultDL && cfg.defaultDeadline > 0 {
		unary = append([]grpc.UnaryServerInterceptor{interceptor.DeadlineUnary(cfg.defaultDeadline)}, unary...)
		stream = append([]grpc.StreamServerInterceptor{interceptor.DeadlineStream(cfg.defaultDeadline)}, stream...)
	}
	if !cfg.disableMetrics {
		metrics := interceptor.NewGRPCMetrics(cfg.metricsRegisterer)
		unary = append([]grpc.UnaryServerInterceptor{metrics.UnaryInterceptor()}, unary...)
		stream = append([]grpc.StreamServerInterceptor{metrics.StreamInterceptor()}, stream...)
	}
	if !cfg.disableLogging {
		logLogger := cfg.loggingLogger
		if logLogger == nil {
			logLogger = slog.Default()
		}
		unary = append([]grpc.UnaryServerInterceptor{interceptor.LoggingUnary(logLogger)}, unary...)
		stream = append([]grpc.StreamServerInterceptor{interceptor.LoggingStream(logLogger)}, stream...)
	}
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
