// Package http is the lazy app-module wrapper for the kit's
// public + internal HTTP server configuration. Services pass
// [http.Module] to [app.Builder.With] to configure TLS, server
// options, the inbound middleware stack, the internal-ops port,
// and custom readiness wiring.
//
// Services that don't register this module run with the kit's
// default HTTP posture: TLS required, default stack on, internal
// ops port loopback-only, no server options, no custom readiness.
// Override any of these by passing options:
//
//	app.New(name, ver, cfg).
//	    With(http.Module(
//	        http.AllowPlaintext(),                  // dev only
//	        http.OptionalClientCertificates(),      // gateway-fronted
//	        http.ReloadingTLS(),                    // hot-rotate certs
//	        http.TLSReloadOnSignal(syscall.SIGHUP), // pair with ReloadingTLS
//	        http.ServerOption(myOpt),
//	        http.StackOptions(stackOpt1, stackOpt2),
//	        http.DisableDefaultStack(),
//	        http.AllowInternalNonLoopback(),        // bind /metrics publicly
//	        http.CustomReadiness(myHandler),
//	    )).
//	    Router(routerFn).
//	    Run()
//
// asvs: V9.1.1, V14.1.1, V14.4.1
package http

import (
	"context"
	"net/http"
	"os"

	"github.com/bds421/rho-kit/app/v2"
	kithttpx "github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "http"

// Option configures [Module].
type Option func(*config)

type config struct {
	allowPlaintext           bool
	optionalClientCerts      bool
	allowInternalNonLoopback bool

	reloadingTLSActive bool
	reloadingTLSOpts   []netutil.FilesCertificateSourceOption
	tlsReloadSignals   []os.Signal

	disableDefaultStack bool
	stackOpts           []stack.Option
	serverOpts          []kithttpx.ServerOption

	customReadiness http.Handler
}

// AllowPlaintext acknowledges that the public HTTP server will run
// without TLS. Without this opt-in, the Builder rejects services
// whose TLS configuration is absent — partial TLS configurations
// silently degrade to plaintext and the always-on validator stops
// the boot.
//
// Use only for services fronted by an external TLS terminator
// (sidecar proxy, ingress) where TLS is enforced by infrastructure
// outside the service binary.
func AllowPlaintext() Option {
	return func(c *config) { c.allowPlaintext = true }
}

// OptionalClientCertificates opts the public TLS server out of
// required-and-verify client auth. The default is
// [tls.RequireAndVerifyClientCert] (FR-014 [HIGH]); call this
// option to relax to [tls.VerifyClientCertIfGiven] when an
// upstream gateway / mesh handles client-cert verification.
//
// This relaxation is the only path off the kit's "TLS env enables
// global mTLS" default, deliberate and documented.
func OptionalClientCertificates() Option {
	return func(c *config) { c.optionalClientCerts = true }
}

// AllowInternalNonLoopback acknowledges that the internal ops
// port (which serves /metrics, /healthz, /ready without
// authentication) will bind to a non-loopback interface.
//
// FR-010 [HIGH]: without this opt-in, the validator refuses any
// internal listener that resolves outside loopback so /metrics
// cannot be exposed on the network by accident.
func AllowInternalNonLoopback() Option {
	return func(c *config) { c.allowInternalNonLoopback = true }
}

// ReloadingTLS enables hot rotation of the TLS material configured
// in [BaseConfig.TLS]. The kit polls the certificate / key / CA
// files for inode changes and rebuilds the TLS config in place,
// so cert rotation no longer requires a restart.
//
// Pair with [TLSReloadOnSignal] to also reload on a signal (the
// poller and signal handler trip the same Reload() entry point so
// concurrent triggers are safe).
func ReloadingTLS(opts ...netutil.FilesCertificateSourceOption) Option {
	cloned := append([]netutil.FilesCertificateSourceOption(nil), opts...)
	return func(c *config) {
		c.reloadingTLSActive = true
		c.reloadingTLSOpts = cloned
	}
}

// TLSReloadOnSignal installs a signal handler that calls
// FilesCertificateSource.Reload when any of signals is received.
// Use alongside [ReloadingTLS]; calling without [ReloadingTLS]
// causes [Builder.Validate] to reject the configuration (there is
// nothing to reload without a reloading source).
//
// Panics if signals is empty or contains nil.
func TLSReloadOnSignal(signals ...os.Signal) Option {
	if len(signals) == 0 {
		panic("app/http: TLSReloadOnSignal requires at least one signal")
	}
	for _, s := range signals {
		if s == nil {
			panic("app/http: TLSReloadOnSignal signal must not be nil")
		}
	}
	cloned := append([]os.Signal(nil), signals...)
	return func(c *config) { c.tlsReloadSignals = cloned }
}

// DisableDefaultStack opts the public mux out of the kit's
// default middleware stack (correlation ID, recover, security
// headers, request logger, timeout, …). Services that compose
// their own stack should call this so the kit doesn't double-wrap.
func DisableDefaultStack() Option {
	return func(c *config) { c.disableDefaultStack = true }
}

// StackOptions appends options forwarded to [stack.Default] when
// the default stack is enabled. No-op when [DisableDefaultStack]
// is also set.
//
// Panics if any option is nil.
func StackOptions(opts ...stack.Option) Option {
	for _, opt := range opts {
		if opt == nil {
			panic("app/http: StackOptions option must not be nil")
		}
	}
	cloned := append([]stack.Option(nil), opts...)
	return func(c *config) { c.stackOpts = append(c.stackOpts, cloned...) }
}

// ServerOption appends a [kithttpx.ServerOption] to the public
// HTTP server. Useful for overriding read / write / idle timeouts,
// disabling HTTP/2, etc. The kit's hardened defaults are applied
// AFTER caller options so security-critical settings cannot be
// silently relaxed.
//
// Panics if opt is nil.
func ServerOption(opt kithttpx.ServerOption) Option {
	if opt == nil {
		panic("app/http: ServerOption requires a non-nil option")
	}
	return func(c *config) { c.serverOpts = append(c.serverOpts, opt) }
}

// CustomReadiness overrides the auto-generated readiness handler.
// The default builds a JSON readiness response from every module's
// HealthChecks() output. Use this when the service needs per-
// component health introspection (e.g., per-observer scan state).
//
// Panics if h is nil.
func CustomReadiness(h http.Handler) Option {
	if h == nil {
		panic("app/http: CustomReadiness requires a non-nil handler")
	}
	return func(c *config) { c.customReadiness = h }
}

// Module returns an [app.Module] carrying the HTTP server
// configuration. The Builder reads the configuration via the
// [app.HTTPConfigProvider] capability and applies it to the
// public + internal HTTP servers it constructs.
//
// Pass nothing for kit defaults (TLS required, default stack on,
// internal ops loopback-only).
func Module(opts ...Option) app.Module {
	cfg := config{}
	for _, opt := range opts {
		if opt == nil {
			panic("app/http: Module option must not be nil")
		}
		opt(&cfg)
	}
	return &httpModule{cfg: cfg}
}

type httpModule struct {
	cfg config
}

func (m *httpModule) Name() string                                  { return ModuleName }
func (m *httpModule) Init(_ context.Context, _ app.ModuleContext) error { return nil }
func (m *httpModule) Populate(_ *app.Infrastructure)                {}
func (m *httpModule) Stop(_ context.Context) error                  { return nil }
func (m *httpModule) HealthChecks() []health.DependencyCheck        { return nil }

// HTTPConfigProvider implementation — the Builder reads the
// resolved configuration through these methods.
func (m *httpModule) AllowPlaintext() bool           { return m.cfg.allowPlaintext }
func (m *httpModule) OptionalClientCerts() bool      { return m.cfg.optionalClientCerts }
func (m *httpModule) AllowInternalNonLoopback() bool { return m.cfg.allowInternalNonLoopback }
func (m *httpModule) ReloadingTLSOptions() ([]netutil.FilesCertificateSourceOption, bool) {
	if !m.cfg.reloadingTLSActive {
		return nil, false
	}
	return m.cfg.reloadingTLSOpts, true
}
func (m *httpModule) TLSReloadSignals() []os.Signal { return m.cfg.tlsReloadSignals }
func (m *httpModule) DisableDefaultStack() bool     { return m.cfg.disableDefaultStack }
func (m *httpModule) StackOptions() []stack.Option  { return m.cfg.stackOpts }
func (m *httpModule) ServerOptions() []kithttpx.ServerOption {
	return m.cfg.serverOpts
}
func (m *httpModule) CustomReadiness() http.Handler { return m.cfg.customReadiness }
