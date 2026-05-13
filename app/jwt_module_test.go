package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/tracing"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
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

// The JWT module no longer reads KIT_ENV. The Builder's always-on
// validator rejects WithJWT without WithJWTIssuer / WithoutJWTIssuer
// upstream, so the module constructor itself only panics on impossible
// inputs (empty URL).
func TestNewJWTModule_AllowsAnyConfigurationOnceURLIsSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  jwtModuleConfig
	}{
		{
			name: "with-issuer",
			cfg: jwtModuleConfig{
				jwksURL:        "https://example.com/.well-known/jwks.json",
				expectedIssuer: "https://issuer.example.com",
			},
		},
		{
			name: "allow-any-issuer",
			cfg: jwtModuleConfig{
				jwksURL:        "https://example.com/.well-known/jwks.json",
				allowAnyIssuer: true,
			},
		},
		{
			name: "with-audience",
			cfg: jwtModuleConfig{
				jwksURL:        "https://example.com/.well-known/jwks.json",
				expectedIssuer: "https://issuer.example.com",
				audience:       "svc",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newJWTModule(tc.cfg)
			require.NotNil(t, m)
		})
	}
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

	modules := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "jwt"), "jwt module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should be present")
}

func TestBuildIntegrationModules_NoJWT(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules := b.buildIntegrationModules()
	assert.False(t, hasModule(modules, "jwt"), "jwt should not be present without config")
}

func TestJWTModuleLogsDoNotExposeIdentityURLs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	httpClient := newHTTPClientModule(false)
	require.NoError(t, httpClient.Init(context.Background(), ModuleContext{
		Logger: logger,
		Config: BaseConfig{},
	}))

	m := newJWTModule(jwtModuleConfig{
		jwksURL:        "https://identity.example.com/realms/acme/.well-known/jwks.json",
		expectedIssuer: "https://identity.example.com/realms/acme",
		audience:       "orders-api",
	})
	require.NoError(t, m.Init(context.Background(), ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
		Config: BaseConfig{},
		modules: map[string]Module{
			"httpclient": httpClient,
		},
	}))

	rendered := buf.String()
	for _, leaked := range []string{
		"identity.example.com",
		"realms/acme",
		".well-known/jwks.json",
		"orders-api",
	} {
		assert.NotContains(t, rendered, leaked)
	}
	assert.Contains(t, rendered, "jwks_configured=true")
	assert.Contains(t, rendered, "issuer_configured=true")
	assert.Contains(t, rendered, "audience_configured=true")
}

func TestBuildIntegrationModules_FullChain(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"}).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")

	modules := b.buildIntegrationModules()
	names := moduleNames(modules)
	// Order: tracing -> httpclient -> jwt
	assert.Equal(t, []string{"tracing", "httpclient", "jwt"}, names)
}

// tracingConfigForTest returns a tracing.Config suitable for unit tests.
// Uses an empty endpoint so a noop provider is created (no network calls).
func tracingConfigForTest() tracing.Config {
	return tracing.Config{ServiceName: "test"}
}

// TestJWTModule_RegistersJWKSMetricsCollector verifies the module wires the
// JWKS observability collector into the configured registerer at Init time.
// Failure to register must NOT block startup (the module still wires the
// verifier) — only the dashboards degrade.
func TestJWTModule_RegistersJWKSMetricsCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	httpClient := newHTTPClientModule(false)
	require.NoError(t, httpClient.Init(context.Background(), ModuleContext{
		Logger: slog.Default(),
		Config: BaseConfig{},
	}))

	m := newJWTModule(jwtModuleConfig{
		jwksURL:        "https://identity.example.com/jwks.json",
		expectedIssuer: "https://issuer.example.com",
		audience:       "svc",
	})
	m.registerer = reg

	require.NoError(t, m.Init(context.Background(), ModuleContext{
		Logger: slog.Default(),
		Runner: lifecycle.NewRunner(slog.Default()),
		Config: BaseConfig{},
		modules: map[string]Module{
			"httpclient": httpClient,
		},
	}))

	families, err := reg.Gather()
	require.NoError(t, err)
	names := make(map[string]bool, len(families))
	for _, mf := range families {
		names[mf.GetName()] = true
	}
	assert.True(t, names["jwks_last_successful_fetch_timestamp_seconds"],
		"jwks_last_successful_fetch_timestamp_seconds missing")
	assert.True(t, names["jwks_fetch_failures_total"],
		"jwks_fetch_failures_total missing")
	assert.True(t, names["jwks_staleness_seconds"],
		"jwks_staleness_seconds missing")
}
