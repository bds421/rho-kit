package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/tracing"
)

func TestJWTModule_Name(t *testing.T) {
	m := newJWTModule(jwtModuleConfig{
		jwksURL:        "https://example.com/.well-known/jwks.json",
		expectedIssuer: "https://issuer.example.com",
	})
	assert.Equal(t, "jwt", m.Name())
}

func TestNewJWTModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for empty JWKS URL")
		assert.Contains(t, fmt.Sprint(r), "non-empty JWKS URL")
	}()
	newJWTModule(jwtModuleConfig{})
}

func TestNewJWTModule_PanicsInProductionWithoutIssuer(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic in production without issuer")
		assert.Contains(t, fmt.Sprint(r), "WithJWTIssuer")
	}()
	newJWTModule(jwtModuleConfig{
		jwksURL: "https://example.com/.well-known/jwks.json",
		// neither expectedIssuer nor allowAnyIssuer set
	})
}

// H-8: KIT_ENV=prod (and any non-development value) must trigger the
// issuer-enforcement panic, not just the literal string "production".
// This aligns the app layer with security/jwtutil's kitcfg.IsDevelopment
// check so the two layers agree on what "production" means.
func TestJWTModule_KitEnvProd_TriggersIssuerCheck(t *testing.T) {
	t.Setenv("KIT_ENV", "prod")
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for KIT_ENV=prod without issuer")
		assert.Contains(t, fmt.Sprint(r), "WithJWTIssuer")
	}()
	newJWTModule(jwtModuleConfig{
		jwksURL: "https://example.com/.well-known/jwks.json",
	})
}

// Mirror of the above for KIT_ENV=staging — any non-development value
// must enforce issuer.
func TestJWTModule_KitEnvStaging_TriggersIssuerCheck(t *testing.T) {
	t.Setenv("KIT_ENV", "staging")
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for KIT_ENV=staging without issuer")
		assert.Contains(t, fmt.Sprint(r), "WithJWTIssuer")
	}()
	newJWTModule(jwtModuleConfig{
		jwksURL: "https://example.com/.well-known/jwks.json",
	})
}

// KIT_ENV=development is the only escape hatch: issuer enforcement
// is skipped to keep dev ergonomics intact.
func TestJWTModule_KitEnvDevelopment_SkipsIssuerCheck(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	m := newJWTModule(jwtModuleConfig{
		jwksURL: "https://example.com/.well-known/jwks.json",
	})
	require.NotNil(t, m)
}

func TestNewJWTModule_AllowsProductionWithExplicitAnyIssuer(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	m := newJWTModule(jwtModuleConfig{
		jwksURL:        "https://example.com/.well-known/jwks.json",
		allowAnyIssuer: true,
	})
	assert.NotNil(t, m)
}

func TestJWTModule_PopulateBeforeInit(t *testing.T) {
	m := newJWTModule(jwtModuleConfig{
		jwksURL:        "https://example.com/.well-known/jwks.json",
		expectedIssuer: "https://issuer.example.com",
	})
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.JWT, "JWT should be nil before Init")
}

func TestBuildIntegrationModules_JWT(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")

	modules, _ := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "jwt"), "jwt module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should be present")
}

func TestBuildIntegrationModules_NoJWT(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, _ := b.buildIntegrationModules()
	assert.False(t, hasModule(modules, "jwt"), "jwt should not be present without config")
}

func TestBuildIntegrationModules_FullChain(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")

	modules, _ := b.buildIntegrationModules()
	names := moduleNames(modules)
	// Order: tracing -> httpclient -> jwt
	assert.Equal(t, []string{"tracing", "httpclient", "jwt"}, names)
}

// tracingConfigForTest returns a tracing.Config suitable for unit tests.
// Uses an empty endpoint so a noop provider is created (no network calls).
func tracingConfigForTest() tracing.Config {
	return tracing.Config{ServiceName: "test"}
}
