package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/tracing"
)

func TestJWTModule_Name(t *testing.T) {
	m := newJWTModule("https://example.com/.well-known/jwks.json")
	assert.Equal(t, "jwt", m.Name())
}

func TestNewJWTModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for empty JWKS URL")
		assert.Contains(t, fmt.Sprint(r), "non-empty JWKS URL")
	}()
	newJWTModule("")
}

func TestJWTModule_PopulateBeforeInit(t *testing.T) {
	m := newJWTModule("https://example.com/.well-known/jwks.json")
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.JWT, "JWT should be nil before Init")
}

func TestBuildIntegrationModules_JWT(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithJWT("https://example.com/.well-known/jwks.json")

	modules, _, _ := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "jwt"), "jwt module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should be present")
}

func TestBuildIntegrationModules_NoJWT(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, _, _ := b.buildIntegrationModules()
	assert.False(t, hasModule(modules, "jwt"), "jwt should not be present without config")
}

func TestBuildIntegrationModules_FullChain(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"}).
		WithJWT("https://example.com/.well-known/jwks.json")

	modules, _, _ := b.buildIntegrationModules()
	names := moduleNames(modules)
	// Order: tracing -> httpclient -> jwt
	assert.Equal(t, []string{"tracing", "httpclient", "jwt"}, names)
}

// tracingConfigForTest returns a tracing.Config suitable for unit tests.
// Uses an empty endpoint so a noop provider is created (no network calls).
func tracingConfigForTest() tracing.Config {
	return tracing.Config{ServiceName: "test"}
}
