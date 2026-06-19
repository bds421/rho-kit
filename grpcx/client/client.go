package client

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
	cliint "github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// DefaultClientDeadline matches the server-side [grpcx.DefaultRPCDeadline]
// so a kit client and kit server agree on the default unary-call timeout.
// Override with [WithDefaultTimeout]; opt out with [WithoutDefaultDeadline].
//
// This is a PER-ATTEMPT deadline. When [WithRetry] is enabled, retry wraps
// the deadline interceptor (see the chain order on [NewClient]), so each
// retry attempt receives a fresh now+DefaultClientDeadline budget rather
// than sharing one across the whole call. To cap the total wall-clock time
// across all attempts, pass your own context.WithTimeout at the call site —
// the deadline interceptor preserves a caller deadline that is tighter than
// now+DefaultClientDeadline.
const DefaultClientDeadline = 30 * time.Second

// defaultKeepalive returns production-safe client keepalive: send a
// keepalive ping every 30s of inactivity, fail if no ACK within 10s.
// PermitWithoutStream so idle connections don't get torn down between
// unary calls.
func defaultKeepalive() keepalive.ClientParameters {
	return keepalive.ClientParameters{
		Time:                30 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}
}

// Option configures the gRPC client returned by [NewClient].
type Option func(*clientConfig)

type clientConfig struct {
	tlsConfig         *tls.Config
	insecureLoopback  bool
	unaryInterceptors []grpc.UnaryClientInterceptor
	streamInts        []grpc.StreamClientInterceptor
	dialOpts          []grpc.DialOption
	defaultDeadline   time.Duration
	disableDefaultDL  bool
	disableRecovery   bool
	recoveryLogger    *slog.Logger
	disableLogging    bool
	loggingLogger     *slog.Logger
	disableMetrics    bool
	metricsRegisterer prometheus.Registerer
	enableRetry       bool
	retryPolicy       retry.Policy
	retryCodes        []codes.Code
	keepaliveParams   *keepalive.ClientParameters
}

// WithTLSConfig pins the *tls.Config used for transport credentials.
// MinVersion is floored to TLS 1.2; [tls.Config.InsecureSkipVerify]
// is rejected. Callers using the kit's Builder typically pass
// [app.Infrastructure.ClientTLS] — already floored to TLS 1.3 via
// [security/netutil].
//
// Panics if cfg is nil. Use [WithInsecure] for loopback dev.
func WithTLSConfig(cfg *tls.Config) Option {
	if cfg == nil {
		panic("grpcx/client: WithTLSConfig requires a non-nil *tls.Config")
	}
	return func(c *clientConfig) { c.tlsConfig = cfg }
}

// WithInsecure dials the target without TLS. Panics inside [NewClient]
// if the target is not a loopback address — the kit refuses to dial a
// remote address in clear text. Use only for tests and local fixtures.
func WithInsecure() Option {
	return func(c *clientConfig) { c.insecureLoopback = true }
}

// WithUnaryInterceptors appends caller-supplied unary interceptors to
// the chain (innermost — closest to the actual invoker).
func WithUnaryInterceptors(ints ...grpc.UnaryClientInterceptor) Option {
	for _, i := range ints {
		if i == nil {
			panic("grpcx/client: WithUnaryInterceptors requires non-nil interceptors")
		}
	}
	owned := append([]grpc.UnaryClientInterceptor(nil), ints...)
	return func(c *clientConfig) {
		c.unaryInterceptors = append(c.unaryInterceptors, owned...)
	}
}

// WithStreamInterceptors appends caller-supplied stream interceptors.
func WithStreamInterceptors(ints ...grpc.StreamClientInterceptor) Option {
	for _, i := range ints {
		if i == nil {
			panic("grpcx/client: WithStreamInterceptors requires non-nil interceptors")
		}
	}
	owned := append([]grpc.StreamClientInterceptor(nil), ints...)
	return func(c *clientConfig) {
		c.streamInts = append(c.streamInts, owned...)
	}
}

// WithDialOptions appends raw grpc.DialOption values for callers
// needing options not surfaced by the typed shape (e.g. custom
// resolver, custom service config). Applied AFTER the kit-hardened
// dial options so the typed-option set wins for non-additive setters.
func WithDialOptions(opts ...grpc.DialOption) Option {
	for _, o := range opts {
		if o == nil {
			panic("grpcx/client: WithDialOptions requires non-nil options")
		}
	}
	owned := append([]grpc.DialOption(nil), opts...)
	return func(c *clientConfig) {
		c.dialOpts = append(c.dialOpts, owned...)
	}
}

// WithDefaultTimeout overrides [DefaultClientDeadline]. Panics on
// non-positive d.
func WithDefaultTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("grpcx/client: WithDefaultTimeout requires positive duration")
	}
	return func(c *clientConfig) { c.defaultDeadline = d }
}

// WithoutDefaultDeadline opts out of the per-RPC default deadline.
// Strongly discouraged in production: a client that forgets
// context.WithTimeout can hang indefinitely on a slow server. Reserved
// for callers that ALWAYS pass their own deadline at the call site.
func WithoutDefaultDeadline() Option {
	return func(c *clientConfig) { c.disableDefaultDL = true }
}

// WithoutRecovery disables the panic-recovery interceptors that
// [NewClient] installs by default. Discouraged: a panic in a custom
// caller interceptor unwinds the goroutine without a structured log.
func WithoutRecovery() Option {
	return func(c *clientConfig) { c.disableRecovery = true }
}

// WithRecoveryLogger overrides the logger used by the recovery
// interceptor. Defaults to slog.Default().
func WithRecoveryLogger(l *slog.Logger) Option {
	return func(c *clientConfig) { c.recoveryLogger = l }
}

// WithLogger overrides the logger used by the logging interceptor.
// Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *clientConfig) { c.loggingLogger = l }
}

// WithoutLogging disables the logging interceptors that [NewClient]
// installs by default.
func WithoutLogging() Option {
	return func(c *clientConfig) { c.disableLogging = true }
}

// WithMetricsRegisterer pins the Prometheus registerer used for the
// client metrics interceptor. Defaults to prometheus.DefaultRegisterer.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	if reg == nil {
		panic("grpcx/client: WithMetricsRegisterer requires non-nil registerer")
	}
	return func(c *clientConfig) { c.metricsRegisterer = reg }
}

// WithoutMetrics disables the metrics interceptor.
func WithoutMetrics() Option {
	return func(c *clientConfig) { c.disableMetrics = true }
}

// WithRetry enables the unary retry interceptor with the given policy.
// Defaults to [interceptor.DefaultRetryableCodes] for which codes to
// retry on. Override via [WithRetryableCodes]. Stream RPCs are not
// auto-retried by this option.
//
// Retry wraps the deadline interceptor (see the chain order on
// [NewClient]), so [DefaultClientDeadline] (or [WithDefaultTimeout]) is a
// PER-ATTEMPT budget, not a total budget: each attempt gets a fresh now+d
// deadline. With the default policy and no caller deadline, a fully
// retried call can therefore run up to attempts*d plus the policy's
// inter-attempt backoff. To bound total wall-clock time across all
// attempts, pass your own context.WithTimeout at the call site.
func WithRetry(policy retry.Policy) Option {
	return func(c *clientConfig) {
		c.enableRetry = true
		c.retryPolicy = policy
	}
}

// WithRetryableCodes overrides the default retryable code set.
// Implies [WithRetry] using [retry.DefaultPolicy] if WithRetry was not
// also passed.
func WithRetryableCodes(cs ...codes.Code) Option {
	if len(cs) == 0 {
		panic("grpcx/client: WithRetryableCodes requires at least one code")
	}
	owned := append([]codes.Code(nil), cs...)
	return func(c *clientConfig) {
		if !c.enableRetry {
			c.enableRetry = true
			c.retryPolicy = retry.DefaultPolicy()
		}
		c.retryCodes = owned
	}
}

// WithKeepaliveParams overrides the default keepalive parameters.
func WithKeepaliveParams(p keepalive.ClientParameters) Option {
	return func(c *clientConfig) { c.keepaliveParams = &p }
}

// NewClient dials target with kit defaults: TLS-only by default (or
// loopback insecure via [WithInsecure]), default per-RPC deadline,
// keepalive, recovery + correlation/request-ID propagation + logging +
// metrics interceptors, optional retry on UNAVAILABLE /
// RESOURCE_EXHAUSTED / ABORTED, plus the caller's interceptor chain.
//
// ID propagation is always on (independent of [WithoutLogging]) so
// disabling logging never severs end-to-end correlation/request-ID
// joins across services.
//
// Final interceptor chain (outermost first):
//
//	recovery -> propagation -> logging -> metrics -> retry (optional) -> deadline -> caller -> RPC
//
// Panics on misconfiguration (no credentials, insecure-on-non-loopback,
// invalid target). Returns the connection without blocking; the first
// RPC discovers connectivity failures.
func NewClient(target string, opts ...Option) (*grpc.ClientConn, error) {
	if strings.TrimSpace(target) == "" {
		panic("grpcx/client: NewClient requires a non-empty target")
	}
	cfg := clientConfig{defaultDeadline: DefaultClientDeadline}
	for _, opt := range opts {
		if opt == nil {
			panic("grpcx/client: NewClient option must not be nil")
		}
		opt(&cfg)
	}

	creds, err := resolveCredentials(target, &cfg)
	if err != nil {
		return nil, err
	}

	kp := defaultKeepalive()
	if cfg.keepaliveParams != nil {
		kp = *cfg.keepaliveParams
	}

	unary := append([]grpc.UnaryClientInterceptor(nil), cfg.unaryInterceptors...)
	stream := append([]grpc.StreamClientInterceptor(nil), cfg.streamInts...)

	// Inner-to-outer prepends so the chain ends up with recovery as
	// the outermost guard.
	if !cfg.disableDefaultDL && cfg.defaultDeadline > 0 {
		unary = append([]grpc.UnaryClientInterceptor{cliint.DeadlineUnary(cfg.defaultDeadline)}, unary...)
		stream = append([]grpc.StreamClientInterceptor{cliint.DeadlineStream(cfg.defaultDeadline)}, stream...)
	}
	if cfg.enableRetry {
		retryOpts := []cliint.RetryOption{cliint.WithRetryPolicy(cfg.retryPolicy)}
		if len(cfg.retryCodes) > 0 {
			retryOpts = append(retryOpts, cliint.WithRetryableCodes(cfg.retryCodes...))
		}
		unary = append([]grpc.UnaryClientInterceptor{cliint.RetryUnary(retryOpts...)}, unary...)
	}
	if !cfg.disableMetrics {
		var mopts []cliint.MetricsOption
		if cfg.metricsRegisterer != nil {
			mopts = append(mopts, cliint.WithRegisterer(cfg.metricsRegisterer))
		}
		metrics := cliint.NewMetrics(mopts...)
		unary = append([]grpc.UnaryClientInterceptor{metrics.UnaryInterceptor()}, unary...)
		stream = append([]grpc.StreamClientInterceptor{metrics.StreamInterceptor()}, stream...)
	}
	if !cfg.disableLogging {
		l := cfg.loggingLogger
		if l == nil {
			l = slog.Default()
		}
		unary = append([]grpc.UnaryClientInterceptor{cliint.LoggingUnary(l)}, unary...)
		stream = append([]grpc.StreamClientInterceptor{cliint.LoggingStream(l)}, stream...)
	}
	// Correlation/request-ID propagation is always on and runs ahead of
	// logging so disabling logging never severs end-to-end trace joins.
	unary = append([]grpc.UnaryClientInterceptor{cliint.PropagationUnaryClientInterceptor()}, unary...)
	stream = append([]grpc.StreamClientInterceptor{cliint.PropagationStreamClientInterceptor()}, stream...)
	if !cfg.disableRecovery {
		l := cfg.recoveryLogger
		if l == nil {
			l = slog.Default()
		}
		unary = append([]grpc.UnaryClientInterceptor{cliint.RecoveryUnary(l)}, unary...)
		stream = append([]grpc.StreamClientInterceptor{cliint.RecoveryStream(l)}, stream...)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(kp),
	}
	if len(unary) > 0 {
		dialOpts = append(dialOpts, grpc.WithChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		dialOpts = append(dialOpts, grpc.WithChainStreamInterceptor(stream...))
	}

	// Caller-supplied DialOptions go AFTER kit hardening so callers can
	// add (e.g. service config) but cannot silently undo keepalive or
	// credentials.
	dialOpts = append(dialOpts, cfg.dialOpts...)
	dialOpts = append(dialOpts,
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(kp),
	)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpcx/client: NewClient dial: %w", err)
	}
	return conn, nil
}

func resolveCredentials(target string, cfg *clientConfig) (credentials.TransportCredentials, error) {
	if cfg.insecureLoopback {
		if !isLoopback(target) {
			panic("grpcx/client: WithInsecure requires a loopback target")
		}
		return insecure.NewCredentials(), nil
	}
	if cfg.tlsConfig == nil {
		panic("grpcx/client: NewClient requires WithTLSConfig (or WithInsecure for loopback)")
	}
	floored, err := tlsclone.ConfigWithFloor(cfg.tlsConfig, tls.VersionTLS12)
	if err != nil {
		return nil, fmt.Errorf("grpcx/client: TLS config: %w", err)
	}
	return credentials.NewTLS(floored), nil
}

func isLoopback(target string) bool {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
