package jwt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
)

const testJWKS = "https://issuer.example.com/.well-known/jwks.json"

func TestModule_Name(t *testing.T) {
	m := Module(testJWKS, WithIssuer("https://idp"), WithAudience("svc"))
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "jwt", m.Name())
}

func TestModule_PanicsOnEmptyJWKSURL(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: Module requires a non-empty jwksURL", func() {
		Module("", WithIssuer("i"), WithAudience("a"))
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: Module option must not be nil", func() {
		Module(testJWKS, nil)
	})
}

func TestModule_PanicsWhenIssuerPolicyMissing(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: Module: pass WithIssuer(...) or WithoutIssuer() to acknowledge issuer policy", func() {
		Module(testJWKS, WithAudience("svc"))
	})
}

func TestModule_PanicsWhenAudiencePolicyMissing(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: Module: pass WithAudience(...) or WithoutAudience() to acknowledge audience policy (RFC 7519 confused-deputy)", func() {
		Module(testJWKS, WithIssuer("https://idp"))
	})
}

func TestWithIssuer_PanicsOnEmpty(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: WithIssuer requires a non-empty issuer (use WithoutIssuer to opt out)", func() {
		WithIssuer("")
	})
}

func TestWithAudience_PanicsOnEmpty(t *testing.T) {
	assert.PanicsWithValue(t, "app/jwt: WithAudience requires a non-empty audience (use WithoutAudience to opt out)", func() {
		WithAudience("")
	})
}

func TestModule_BuildsWithIssuerAndAudience(t *testing.T) {
	// Construction should succeed when both policy pairs are pinned.
	assert.NotPanics(t, func() {
		Module(testJWKS,
			WithIssuer("https://idp.example.com"),
			WithAudience("backend"),
		)
	})
}

func TestModule_BuildsWithOptOuts(t *testing.T) {
	assert.NotPanics(t, func() {
		Module(testJWKS, WithoutIssuer(), WithoutAudience())
	})
}

func TestProvider_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Provider(infra),
		"Provider(infra) must return nil when no jwt module was registered")
}

func TestModule_ImplementsModule(t *testing.T) {
	// Type-check the Module return implements app.Module — the
	// interface satisfaction is the assertion, not the value.
	m := Module(testJWKS, WithIssuer("https://i"), WithAudience("a"))
	if _, ok := any(m).(app.Module); !ok {
		t.Fatal("Module() must return app.Module")
	}
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module(testJWKS, WithoutIssuer(), WithoutAudience())
	// Stop before Init must not panic.
	require.NotPanics(t, func() {
		_ = m.Stop(context.Background())
	})
}
