package grpcx

import (
	"crypto/tls"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
	"github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

// ServerOption configures the gRPC server returned by NewServer.
type ServerOption func(*serverConfig)

type serverConfig struct {
	unaryInterceptors    []grpc.UnaryServerInterceptor
	streamInterceptors   []grpc.StreamServerInterceptor
	grpcOpts             []grpc.ServerOption
	maxRecvMsgSize       int
	maxSendMsgSize       int
	maxConcurrentStreams uint32
	maxHeaderListSize    uint32
	keepaliveParams      *keepalive.ServerParameters
	keepalivePolicy      *keepalive.EnforcementPolicy
	disableRecovery      bool
	recoveryLogger       *slog.Logger
	defaultDeadline      time.Duration
	streamDeadline       *time.Duration // nil = same as unary; 0 = no stream deadline
	disableDefaultDL     bool
	disableLogging       bool
	loggingLogger        *slog.Logger
	disableMetrics       bool
	metricsRegisterer    prometheus.Registerer
	enableReflection     bool
	tlsConfig            *tls.Config
	maxServerStreams     int
	streamIdleTimeout    time.Duration
	hasTransportCreds    bool
}

// DefaultRPCDeadline is the per-RPC deadline applied automatically by
// [NewServer] when the caller does not configure one explicitly. It bounds
// every handler so a streaming RPC or a unary RPC from a crashed client
// cannot pin a goroutine indefinitely. Override with [WithDefaultTimeout]
// or opt out with [WithoutDefaultDeadline].
const DefaultRPCDeadline = 30 * time.Second

const (
	// defaultMaxRecvMsgSize is 4 MB, matching the gRPC default.
	defaultMaxRecvMsgSize = 4 << 20

	// defaultMaxSendMsgSize is 4 MB, matching the gRPC default.
	defaultMaxSendMsgSize = 4 << 20

	// defaultMaxConcurrentStreams caps the number of in-flight HTTP/2 streams
	// per TCP connection. Go's gRPC default is math.MaxUint32, so a single
	// peer can open ~4B streams, each pinning a goroutine + stream state
	// until the per-RPC deadline fires — the GAP-03 streaming-flood vector
	// in docs/audit/THREAT_MODEL.md. 1000 is a conservative production
	// ceiling; override with [WithMaxConcurrentStreams] for fan-in proxies.
	defaultMaxConcurrentStreams uint32 = 1000

	// defaultMaxHeaderListSize caps the total uncompressed size of HTTP/2
	// header blocks per stream. Go's gRPC server defaults to 16 MiB, large
	// enough that a single client can pin tens of MiB of decompression
	// state per stream. 1 MiB matches the httpx HTTP/1.1 header ceiling
	// (MaxHeaderBytes), keeping the cross-protocol header-flood budget
	// consistent.
	defaultMaxHeaderListSize uint32 = 1 << 20
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
	// MinTime is strictly below the paired client default keepalive
	// Time (30s) so client pings are never treated as too frequent
	// under equal clocks (grpc-go GOAWAY risk when MinTime == client Time).
	return keepalive.EnforcementPolicy{
		MinTime:             20 * time.Second,
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

// WithMaxConcurrentStreams overrides the per-connection HTTP/2 stream cap
// (default [defaultMaxConcurrentStreams]). The kit pins this away from
// Go gRPC's math.MaxUint32 default to close the GAP-03 streaming-flood
// vector documented in docs/audit/THREAT_MODEL.md §4.2 — a single peer
// can otherwise open billions of streams, each pinning a goroutine and
// stream-state memory until the per-RPC deadline fires.
//
// Raise the cap only for trusted fan-in proxies where the upstream
// connection multiplexes many client connections through a single TCP
// session. Panics if n is 0 (the gRPC framework treats 0 as "unlimited",
// which silently undoes the hardening).
func WithMaxConcurrentStreams(n uint32) ServerOption {
	if n == 0 {
		panic("grpcx: WithMaxConcurrentStreams requires a positive limit (0 means unlimited)")
	}
	return func(c *serverConfig) { c.maxConcurrentStreams = n }
}

// WithKeepaliveParams replaces the entire default keepalive parameter set.
// Zero fields are passed through to grpc-go (where 0 often means "disabled" /
// infinity). Merge intentionally: set every field you care about, not just the
// one being tuned, or MaxConnectionIdle/Age hardening from the defaults is lost.
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
		// Prefer [WithTLSConfig]; flag so plaintext-startup warning is suppressed
		// when callers smuggle credentials via raw options.
		c.hasTransportCreds = true
	}
}

// WithTLSConfig installs transport credentials floored to TLS 1.2+,
// matching client.NewClient's WithTLSConfig. Prefer this over raw
// WithGRPCServerOptions(grpc.Creds(...)) so the kit TLS floor is applied.
// Panics on a nil config.
func WithTLSConfig(cfg *tls.Config) ServerOption {
	if cfg == nil {
		panic("grpcx: WithTLSConfig requires a non-nil *tls.Config")
	}
	return func(c *serverConfig) {
		c.tlsConfig = cfg
		c.hasTransportCreds = true
	}
}

// WithMaxServerStreams installs a server-wide concurrent streaming-RPC
// cap via interceptor.MaxConcurrentStreamsServer. Independent of gRPC's
// per-connection MaxConcurrentStreams. Panics on max <= 0.
func WithMaxServerStreams(max int) ServerOption {
	if max <= 0 {
		panic("grpcx: WithMaxServerStreams requires a positive max")
	}
	return func(c *serverConfig) {
		c.maxServerStreams = max
	}
}

// WithStreamIdleTimeout installs interceptor.StreamIdleTimeout so streams
// with no send/recv for d are cancelled. Panics on d <= 0.
func WithStreamIdleTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("grpcx: WithStreamIdleTimeout requires a positive duration")
	}
	return func(c *serverConfig) {
		c.streamIdleTimeout = d
	}
}

// WithoutRecovery disables the panic-recovery interceptors that NewServer
// installs by default. Strongly discouraged in production: a handler panic
// will tear down the gRPC connection without a structured log entry. Use
// only for tests that intentionally observe panic propagation.
func WithoutRecovery() ServerOption {
	return func(c *serverConfig) { c.disableRecovery = true }
}

// WithReflection registers the gRPC server reflection service
// (google.golang.org/grpc/reflection) on the returned server. Off by
// default because reflection exposes the full schema of registered
// services — useful for grpcurl / Postman in dev and staging, but a
// fingerprinting surface in production.
//
// Production deployments that need reflection (e.g. internal admin
// tooling) should pair this with an inbound auth interceptor that
// blocks unauthenticated callers from reaching the reflection methods.
//
// Registration order: NewServer enables reflection on the *grpc.Server
// at construction time (before RegisterServices). gRPC interceptors are
// still applied to reflection RPCs because they wrap the server as a
// whole; the previous claim that reflection is registered "after the
// caller's services" was incorrect.
func WithReflection() ServerOption {
	return func(c *serverConfig) { c.enableReflection = true }
}

// WithRecoveryLogger overrides the logger passed to the recovery
// interceptors. Defaults to slog.Default().
func WithRecoveryLogger(l *slog.Logger) ServerOption {
	return func(c *serverConfig) { c.recoveryLogger = l }
}

// WithDefaultTimeout overrides the [DefaultRPCDeadline] applied by
// [NewServer] for the per-RPC default-deadline interceptor on unary RPCs
// (and streams unless [WithDefaultStreamTimeout] is also set). The
// interceptor sets the handler ctx deadline to `now + d` when the inbound
// RPC has no deadline OR has a deadline further out than `now + d`.
// Closer deadlines from the caller are preserved.
//
// Without a server-side cap, a unary RPC from a crashed client can hold a
// handler open indefinitely. Streaming RPCs that need longer life should
// use [WithDefaultStreamTimeout] rather than disabling unary protection.
//
// The interceptor is prepended after recovery so panics still convert to
// codes.Internal but every request lands with a bounded ctx.
//
// Panics if d is not positive.
func WithDefaultTimeout(d time.Duration) ServerOption {
	if d <= 0 {
		panic("grpcx: WithDefaultTimeout requires a positive duration")
	}
	return func(c *serverConfig) { c.defaultDeadline = d }
}

// WithDefaultStreamTimeout sets the absolute deadline applied to streaming
// handlers independently of the unary [WithDefaultTimeout] /
// [DefaultRPCDeadline]. Pass 0 to leave streaming RPCs without an automatic
// absolute deadline (still subject to any caller-supplied deadline and
// optional idle-timeout interceptors). Long-lived watch/bidi streams
// typically want 0 or a large value.
//
// Panics on negative d.
func WithDefaultStreamTimeout(d time.Duration) ServerOption {
	if d < 0 {
		panic("grpcx: WithDefaultStreamTimeout requires a non-negative duration")
	}
	return func(c *serverConfig) { c.streamDeadline = &d }
}

// WithoutDefaultDeadline opts the server out of the [DefaultRPCDeadline]
// auto-applied by [NewServer] for both unary and stream RPCs. Strongly
// discouraged for production — without a server-side cap a slow client can
// pin a handler goroutine forever. Prefer [WithDefaultStreamTimeout](0)
// when only streams need unbounded life.
func WithoutDefaultDeadline() ServerOption {
	return func(c *serverConfig) { c.disableDefaultDL = true }
}

// WithMaxHeaderListSize overrides the hardened HTTP/2 max header list size
// (default 1 MiB). Raw [WithGRPCServerOptions](grpc.MaxHeaderListSize(...))
// values are re-overridden by the kit; use this typed option instead.
//
// Panics if n is 0.
func WithMaxHeaderListSize(n uint32) ServerOption {
	if n == 0 {
		panic("grpcx: WithMaxHeaderListSize requires a positive limit")
	}
	return func(c *serverConfig) { c.maxHeaderListSize = n }
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
// message size limits, a per-connection [defaultMaxConcurrentStreams]
// cap, recovery + logging + metrics interceptors, a per-RPC default
// deadline, and the user-supplied interceptors.
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
		maxRecvMsgSize:       defaultMaxRecvMsgSize,
		maxSendMsgSize:       defaultMaxSendMsgSize,
		maxConcurrentStreams: defaultMaxConcurrentStreams,
		defaultDeadline:      DefaultRPCDeadline,
		maxHeaderListSize:    defaultMaxHeaderListSize,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx: NewServer option must not be nil")
		}
		opt(&cfg)
	}

	if cfg.maxHeaderListSize == 0 {
		cfg.maxHeaderListSize = defaultMaxHeaderListSize
	}
	kp := defaultKeepalive()
	if cfg.keepaliveParams != nil {
		kp = *cfg.keepaliveParams
	}

	ep := defaultEnforcementPolicy()
	if cfg.keepalivePolicy != nil {
		ep = *cfg.keepalivePolicy
	}

	// Hardened transport options are applied once AFTER cfg.grpcOpts below
	// so raw ServerOptions cannot silently undo kit limits (later options
	// win for non-additive setters). Interceptors are collected first.
	var grpcOpts []grpc.ServerOption

	unary := cfg.unaryInterceptors
	stream := cfg.streamInterceptors

	// Inner-to-outer prepends so the final order matches the chain
	// documented above: caller-supplied interceptors stay closest to
	// the handler; deadline wraps them; metrics wraps deadline;
	// logging wraps metrics; recovery is the outermost guard.
	if !cfg.disableDefaultDL && cfg.defaultDeadline > 0 {
		unary = append([]grpc.UnaryServerInterceptor{interceptor.DeadlineUnary(cfg.defaultDeadline)}, unary...)
	}
	if !cfg.disableDefaultDL {
		streamDL := cfg.defaultDeadline
		if cfg.streamDeadline != nil {
			streamDL = *cfg.streamDeadline
		}
		if streamDL > 0 {
			stream = append([]grpc.StreamServerInterceptor{interceptor.DeadlineStream(streamDL)}, stream...)
		}
	}
	if !cfg.disableMetrics {
		var metricsOpts []interceptor.MetricsOption
		if cfg.metricsRegisterer != nil {
			metricsOpts = append(metricsOpts, interceptor.WithRegisterer(cfg.metricsRegisterer))
		}
		metrics := interceptor.NewMetrics(metricsOpts...)
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

	if cfg.maxServerStreams > 0 || cfg.streamIdleTimeout > 0 {
		var limMetrics *interceptor.StreamLimitMetrics
		if !cfg.disableMetrics {
			var mopts []interceptor.MetricsOption
			if cfg.metricsRegisterer != nil {
				mopts = append(mopts, interceptor.WithRegisterer(cfg.metricsRegisterer))
			}
			limMetrics = interceptor.NewStreamLimitMetrics(mopts...)
		}
		if cfg.maxServerStreams > 0 {
			stream = append([]grpc.StreamServerInterceptor{
				interceptor.MaxConcurrentStreamsServer(cfg.maxServerStreams, limMetrics),
			}, stream...)
		}
		if cfg.streamIdleTimeout > 0 {
			stream = append([]grpc.StreamServerInterceptor{
				interceptor.StreamIdleTimeout(cfg.streamIdleTimeout, limMetrics),
			}, stream...)
		}
	}

	if len(unary) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainStreamInterceptor(stream...))
	}

	// Raw caller-supplied options must NOT silently undo kit-hardened
	// defaults (max recv/send message size, keepalive params, header
	// list size). grpc.NewServer applies options in slice order and
	// later options win for non-additive setters, so we re-append the
	// hardened set AFTER cfg.grpcOpts. Callers who genuinely need to
	// override message size limits or keepalive policy should use the
	// typed options (WithMaxRecvMsgSize, WithKeepaliveParams, …) which
	// are baked into the same hardened set below and therefore win
	// over any raw override (L083).
	grpcOpts = append(grpcOpts, cfg.grpcOpts...)
	if cfg.tlsConfig != nil {
		floored, err := tlsclone.ConfigWithFloor(cfg.tlsConfig, tls.VersionTLS12)
		if err != nil {
			panic("grpcx: WithTLSConfig: " + err.Error())
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(floored)))
	} else if !cfg.hasTransportCreds {
		slog.Default().Warn("grpcx: NewServer built without transport credentials; serving plaintext unless a mesh terminates TLS. Use WithTLSConfig for encrypted transport")
	}
	grpcOpts = append(grpcOpts,
		grpc.MaxRecvMsgSize(cfg.maxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.maxSendMsgSize),
		grpc.MaxConcurrentStreams(cfg.maxConcurrentStreams),
		grpc.MaxHeaderListSize(cfg.maxHeaderListSize),
		grpc.KeepaliveParams(kp),
		grpc.KeepaliveEnforcementPolicy(ep),
	)

	srv := grpc.NewServer(grpcOpts...)
	if cfg.enableReflection {
		reflection.Register(srv)
	}
	return srv
}
