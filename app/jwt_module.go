package app

import (
	"context"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/security/jwtutil"
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
// Panics if jwksURL is empty (startup-time configuration error). Panics in
// KIT_ENV=production if neither expectedIssuer nor allowAnyIssuer is set —
// silently disabling issuer enforcement was a known footgun in earlier
// versions and is now opt-in.
func newJWTModule(cfg jwtModuleConfig) *jwtModule {
	if cfg.jwksURL == "" {
		panic("app: jwt module requires a non-empty JWKS URL")
	}
	if cfg.expectedIssuer == "" && !cfg.allowAnyIssuer && os.Getenv("KIT_ENV") == "production" {
		panic("app: WithJWT in KIT_ENV=production requires WithJWTIssuer or WithJWTAllowAnyIssuer (silent issuer-skip is forbidden)")
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
		mc.Logger.Warn("jwt provider configured WITHOUT issuer enforcement", "jwks_url", m.cfg.jwksURL)
	case m.cfg.expectedIssuer != "":
		opts = append(opts, jwtutil.WithExpectedIssuer(m.cfg.expectedIssuer))
	default:
		// Non-production fallback: keep the historical default but warn.
		opts = append(opts, jwtutil.WithExpectedIssuer("https://oathkeeper"))
		mc.Logger.Warn("jwt provider using legacy default issuer; pass WithJWTIssuer for production-safe verification",
			"default_issuer", "https://oathkeeper",
			"jwks_url", m.cfg.jwksURL,
		)
	}
	if m.cfg.audience != "" {
		opts = append(opts, jwtutil.WithExpectedAudience(m.cfg.audience))
	}

	m.provider = jwtutil.NewProvider(
		m.cfg.jwksURL,
		httpClient,
		jwtutil.CacheTTL(),
		opts...,
	)

	mc.Runner.AddFunc("jwt-provider", func(ctx context.Context) error {
		m.provider.Run(ctx)
		return nil
	})

	mc.Logger.Info("jwt provider configured",
		"jwks_url", m.cfg.jwksURL,
		"issuer", m.cfg.expectedIssuer,
		"allow_any_issuer", m.cfg.allowAnyIssuer,
	)
	return nil
}

func (m *jwtModule) Populate(infra *Infrastructure) {
	infra.JWT = m.provider
}
