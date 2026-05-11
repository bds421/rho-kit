package app

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// jwtModuleConfig captures the JWT module options resolved from the Builder.
type jwtModuleConfig struct {
	jwksURL        string
	expectedIssuer string
	allowAnyIssuer bool
	audience       string
}

// jwtModule implements the Module interface for JWT/JWKS verification.
// It reads the HTTP client from the httpClientModule to fetch JWKS keys,
// and registers the provider as a lifecycle component on the Runner.
type jwtModule struct {
	BaseModule

	cfg jwtModuleConfig

	// initialized during Init
	provider *jwtutil.Provider
}

// newJWTModule creates a JWT module from the resolved config.
//
// Panics if jwksURL is empty (startup-time configuration error). The
// issuer + audience pinning is enforced upstream at [Builder.Build]
// time via the always-on production-safety validator; this constructor
// trusts that the Builder has already rejected misconfigurations and
// only flags impossible inputs the validator could not catch.
func newJWTModule(cfg jwtModuleConfig) *jwtModule {
	if cfg.jwksURL == "" {
		panic("app: jwt module requires a non-empty JWKS URL")
	}
	return &jwtModule{
		BaseModule: NewBaseModule("jwt"),
		cfg:        cfg,
	}
}

func (m *jwtModule) Init(_ context.Context, mc ModuleContext) error {
	hcMod, ok := mc.Module("httpclient").(*httpClientModule)
	if !ok {
		return fmt.Errorf("jwt module: httpclient module has unexpected type")
	}

	httpClient := hcMod.Client()

	opts := []jwtutil.ProviderOption{}
	switch {
	case m.cfg.allowAnyIssuer:
		opts = append(opts, jwtutil.WithAllowAnyIssuer())
		mc.Logger.Warn("jwt provider configured WITHOUT issuer enforcement",
			"jwks_configured", m.cfg.jwksURL != "",
		)
	case m.cfg.expectedIssuer != "":
		opts = append(opts, jwtutil.WithExpectedIssuer(m.cfg.expectedIssuer))
	default:
		// This branch is unreachable when the module is constructed
		// via app.Builder — Builder.Validate rejects WithJWT without
		// either WithJWTIssuer or WithoutJWTIssuer. The defensive
		// allow-any path is here for hand-constructed modules; emit
		// an Error-level log so the misconfiguration cannot hide in
		// log volume. Operators monitoring Error-level logs will see
		// it on the first request the provider validates.
		opts = append(opts, jwtutil.WithAllowAnyIssuer())
		mc.Logger.Error("jwt provider built without issuer pin and without explicit opt-out — verifying tokens from any authority. Confused-deputy hazard: a token issued for service A is silently valid at service B. Use Builder.WithJWTIssuer (preferred) or Builder.WithoutJWTIssuer (explicit acknowledgement) to remove this log line.",
			"jwks_configured", m.cfg.jwksURL != "",
		)
	}
	if m.cfg.audience != "" {
		opts = append(opts, jwtutil.WithExpectedAudience(m.cfg.audience))
	} else {
		// Builder.Validate already requires WithJWTAudience or the explicit
		// WithoutJWTAudience opt-out; mirror that into the provider so
		// jwtutil.NewProvider's audience guardrail does not panic on
		// validator-approved configs.
		opts = append(opts, jwtutil.WithAllowAnyAudience())
	}

	m.provider = jwtutil.NewProvider(
		m.cfg.jwksURL,
		httpClient,
		jwtutil.CacheTTL(),
		opts...,
	)

	mc.Runner.AddFunc("jwt-provider", func(ctx context.Context) error {
		return m.provider.Run(ctx)
	})

	mc.Logger.Info("jwt provider configured",
		"jwks_configured", m.cfg.jwksURL != "",
		"issuer_configured", m.cfg.expectedIssuer != "",
		"audience_configured", m.cfg.audience != "",
		"allow_any_issuer", m.cfg.allowAnyIssuer,
	)
	return nil
}

func (m *jwtModule) Populate(infra *Infrastructure) {
	infra.JWT = m.provider
}
