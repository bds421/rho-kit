// Package jwt is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/security/v2/jwtutil].
//
// Services that verify JWTs pass [jwt.Module] to
// [app.Builder.With]; services that don't, do not import this
// package — so the JWKS-refresh goroutine, jwx dependency, and
// JWKS metric collector stay out of binaries that don't need
// them.
//
// Issuer and audience policy MUST be acknowledged explicitly:
//   - Pin via [WithIssuer]/[WithAudience], OR
//   - Opt out via [WithoutIssuer]/[WithoutAudience].
//
// Module construction panics if neither is supplied for either
// axis. This is the same confused-deputy guard the v1 Builder
// surfaced via Builder.Validate; the bridge module moves the
// check to the construction site so the failure mode is
// "service exits at startup" rather than "lifecycle fails at
// runtime".
//
// Retrieve the constructed Provider inside the [app.RouterFunc]
// via [Provider]:
//
//	app.New(name).
//	    With(jwt.Module(jwksURL,
//	        jwt.WithIssuer("https://idp.example.com"),
//	        jwt.WithAudience("backend"))).
//	    Run(func(infra app.Infrastructure) http.Handler { ... })
package jwt

import (
	"context"
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// ResourceProviderKey is the [app.Infrastructure.Resource] key
// under which [Module] publishes its *jwtutil.Provider.
const ResourceProviderKey = "github.com/bds421/rho-kit/app/jwt.provider"

// ModuleName is the registered Module.Name() value.
const ModuleName = "jwt"

// Option configures [Module].
type Option func(*config)

type config struct {
	expectedIssuer   string
	allowAnyIssuer   bool
	audience         string
	allowAnyAudience bool

	registerer prometheus.Registerer
	instance   string
}

// WithIssuer pins the expected JWT issuer. Tokens whose `iss`
// claim does not match are rejected.
//
// Panics if iss is empty — use [WithoutIssuer] to opt out
// explicitly.
func WithIssuer(iss string) Option {
	if iss == "" {
		panic("app/jwt: WithIssuer requires a non-empty issuer (use WithoutIssuer to opt out)")
	}
	return func(c *config) {
		c.expectedIssuer = iss
		c.allowAnyIssuer = false
	}
}

// WithoutIssuer explicitly opts out of issuer pinning. Use this
// only after reviewing the confused-deputy risk: a token issued
// by a different authority that signs with the same JWKS is
// silently valid.
func WithoutIssuer() Option {
	return func(c *config) {
		c.allowAnyIssuer = true
		c.expectedIssuer = ""
	}
}

// WithAudience pins the expected JWT audience. Tokens whose
// `aud` claim does not include the value are rejected.
//
// Panics if aud is empty — use [WithoutAudience] to opt out
// explicitly.
func WithAudience(aud string) Option {
	if aud == "" {
		panic("app/jwt: WithAudience requires a non-empty audience (use WithoutAudience to opt out)")
	}
	return func(c *config) {
		c.audience = aud
		c.allowAnyAudience = false
	}
}

// WithoutAudience explicitly opts out of audience pinning. Use
// only when no audience claim is expected (RFC 7519 confused-
// deputy mitigation is then waived).
func WithoutAudience() Option {
	return func(c *config) {
		c.allowAnyAudience = true
		c.audience = ""
	}
}

// WithRegisterer overrides the prometheus.Registerer used for
// the JWKS metric collector. Default is prometheus.DefaultRegisterer.
//
// Panics if r is nil.
func WithRegisterer(r prometheus.Registerer) Option {
	if r == nil {
		panic("app/jwt: WithRegisterer requires a non-nil Registerer")
	}
	return func(c *config) { c.registerer = r }
}

// Module returns an [app.Module] that builds a [jwtutil.Provider]
// at Init time, runs its JWKS-refresh loop under the lifecycle
// Runner, and publishes the Provider on [app.Infrastructure]
// under [ResourceProviderKey].
//
// Panics:
//   - jwksURL is empty
//   - neither [WithIssuer] nor [WithoutIssuer] is supplied
//   - neither [WithAudience] nor [WithoutAudience] is supplied
//
// All three are confused-deputy guards: failing closed at
// construction time beats silently verifying tokens from any
// authority or for any audience.
func Module(jwksURL string, opts ...Option) app.Module {
	if jwksURL == "" {
		panic("app/jwt: Module requires a non-empty jwksURL")
	}
	cfg := config{instance: "primary"}
	for _, opt := range opts {
		if opt == nil {
			panic("app/jwt: Module option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.expectedIssuer == "" && !cfg.allowAnyIssuer {
		panic("app/jwt: Module: pass WithIssuer(...) or WithoutIssuer() to acknowledge issuer policy")
	}
	if cfg.audience == "" && !cfg.allowAnyAudience {
		panic("app/jwt: Module: pass WithAudience(...) or WithoutAudience() to acknowledge audience policy (RFC 7519 confused-deputy)")
	}
	return &jwtModule{jwksURL: jwksURL, cfg: cfg}
}

type jwtModule struct {
	jwksURL string
	cfg     config

	provider *jwtutil.Provider
}

func (m *jwtModule) Name() string { return ModuleName }

func (m *jwtModule) Init(_ context.Context, mc app.ModuleContext) error {
	hcm, ok := mc.Module("httpclient").(app.HTTPClientProvider)
	if !ok {
		return errors.New("app/jwt: httpclient module not registered or unexpected type")
	}
	httpClient := hcm.Client()

	opts := []jwtutil.ProviderOption{}
	switch {
	case m.cfg.allowAnyIssuer:
		opts = append(opts, jwtutil.WithAllowAnyIssuer())
		mc.Logger.Warn("jwt provider configured WITHOUT issuer enforcement",
			"jwks_configured", m.jwksURL != "",
		)
	case m.cfg.expectedIssuer != "":
		opts = append(opts, jwtutil.WithExpectedIssuer(m.cfg.expectedIssuer))
	}
	if m.cfg.audience != "" {
		opts = append(opts, jwtutil.WithExpectedAudience(m.cfg.audience))
	} else {
		opts = append(opts, jwtutil.WithAllowAnyAudience())
	}

	m.provider = jwtutil.NewProvider(m.jwksURL, httpClient, jwtutil.CacheTTL(), opts...)

	// Register the JWKS observability collector. Registration
	// failures degrade the dashboard but never block startup.
	var metricsOpts []jwtutil.MetricsOption
	if m.cfg.registerer != nil {
		metricsOpts = append(metricsOpts, jwtutil.WithRegisterer(m.cfg.registerer))
	}
	if _, err := jwtutil.NewMetricsCollector(m.provider, m.cfg.instance, metricsOpts...); err != nil {
		mc.Logger.Warn("jwks metrics collector not registered", redact.Error(err))
	}

	mc.Runner.AddFunc("jwt-provider", func(ctx context.Context) error {
		return m.provider.Run(ctx)
	})

	mc.Logger.Info("jwt provider configured",
		"jwks_configured", m.jwksURL != "",
		"issuer_configured", m.cfg.expectedIssuer != "",
		"audience_configured", m.cfg.audience != "",
		"allow_any_issuer", m.cfg.allowAnyIssuer,
		"allow_any_audience", m.cfg.allowAnyAudience,
	)
	return nil
}

func (m *jwtModule) Populate(infra *app.Infrastructure) {
	if m.provider != nil {
		infra.SetResource(ResourceProviderKey, m.provider)
	}
}

func (m *jwtModule) Stop(_ context.Context) error { return nil }

func (m *jwtModule) HealthChecks() []health.DependencyCheck { return nil }

// Provider returns the *jwtutil.Provider published by [Module]
// under [ResourceProviderKey], or nil if [Module] was not
// registered with the Builder.
func Provider(infra app.Infrastructure) *jwtutil.Provider {
	v, ok := infra.Resource(ResourceProviderKey)
	if !ok {
		return nil
	}
	p, _ := v.(*jwtutil.Provider)
	return p
}

