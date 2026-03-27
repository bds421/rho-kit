package app

import (
	"context"
	"fmt"

	"github.com/bds421/rho-kit/security/jwtutil"
)

// jwtModule implements the Module interface for JWT/JWKS verification.
// It reads the HTTP client from the httpClientModule to fetch JWKS keys,
// and registers the provider as a lifecycle component on the Runner.
type jwtModule struct {
	BaseModule

	jwksURL string

	// initialized during Init
	provider *jwtutil.Provider
}

// newJWTModule creates a JWT module with the given JWKS URL.
// Panics if jwksURL is empty (startup-time configuration error).
func newJWTModule(jwksURL string) *jwtModule {
	if jwksURL == "" {
		panic("app: jwt module requires a non-empty JWKS URL")
	}
	return &jwtModule{
		BaseModule: NewBaseModule("jwt"),
		jwksURL:    jwksURL,
	}
}

func (m *jwtModule) Init(_ context.Context, mc ModuleContext) error {
	hcMod, ok := mc.Module("httpclient").(*httpClientModule)
	if !ok {
		return fmt.Errorf("jwt module: httpclient module has unexpected type")
	}

	httpClient := hcMod.Client()
	m.provider = jwtutil.NewProvider(
		m.jwksURL,
		httpClient,
		jwtutil.CacheTTL(),
		jwtutil.WithExpectedIssuer("https://oathkeeper"),
	)

	mc.Runner.AddFunc("jwt-provider", func(ctx context.Context) error {
		m.provider.Run(ctx)
		return nil
	})

	mc.Logger.Info("jwt provider configured", "jwks_url", m.jwksURL)
	return nil
}

func (m *jwtModule) Populate(infra *Infrastructure) {
	infra.JWT = m.provider
}
