package jwt

import (
	"context"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
)

// stubHTTPClient is a minimal module that satisfies both app.Module and
// app.HTTPClientProvider so Init can be exercised under a realistic
// ModuleContext without standing up the real builtin httpclient module
// (which lives unexported inside app).
type stubHTTPClient struct {
	app.BaseModule
	client *http.Client
}

func newStubHTTPClient() *stubHTTPClient {
	return &stubHTTPClient{
		BaseModule: app.NewBaseModule("httpclient"),
		client:     &http.Client{},
	}
}

func (s *stubHTTPClient) Client() *http.Client { return s.client }

// wrongTypeHTTPClient registers under the "httpclient" name but does NOT
// implement HTTPClientProvider, so the type assertion in Init fails.
type wrongTypeHTTPClient struct {
	app.BaseModule
}

func newWrongTypeHTTPClient() *wrongTypeHTTPClient {
	return &wrongTypeHTTPClient{BaseModule: app.NewBaseModule("httpclient")}
}

func initContext(t *testing.T, modules ...app.Module) app.ModuleContext {
	t.Helper()
	mc, err := app.TestModuleContext(modules...)
	require.NoError(t, err)
	return mc
}

// initModule drives a freshly built module's Init against a ModuleContext
// carrying the given peer modules plus a stub httpclient, and returns the
// module so callers can inspect Populate output.
func initModule(t *testing.T, opts []Option, peers ...app.Module) *jwtModule {
	t.Helper()
	mod := Module(testJWKS, opts...)
	jm, ok := mod.(*jwtModule)
	require.True(t, ok, "Module must return *jwtModule")

	mc := initContext(t, append([]app.Module{newStubHTTPClient()}, peers...)...)
	require.NoError(t, jm.Init(context.Background(), mc))
	return jm
}

func TestInit_FailsWhenHTTPClientMissing(t *testing.T) {
	mod := Module(testJWKS, WithoutIssuer(), WithoutAudience())
	// No httpclient module registered: LookupModule returns nil and Init
	// must surface the actionable error rather than panicking.
	mc := initContext(t)
	require.NotPanics(t, func() {
		err := mod.(*jwtModule).Init(context.Background(), mc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "httpclient module not registered")
	})
}

func TestInit_FailsWhenHTTPClientWrongType(t *testing.T) {
	mod := Module(testJWKS, WithoutIssuer(), WithoutAudience())
	mc := initContext(t, newWrongTypeHTTPClient())
	err := mod.(*jwtModule).Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected type")
}

func TestInit_BuildsProviderForAllPolicyCombinations(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"pinned issuer + pinned audience", []Option{WithIssuer("https://idp.example.com"), WithAudience("backend")}},
		{"pinned issuer + any audience", []Option{WithIssuer("https://idp.example.com"), WithoutAudience()}},
		{"any issuer + pinned audience", []Option{WithoutIssuer(), WithAudience("backend")}},
		{"any issuer + any audience", []Option{WithoutIssuer(), WithoutAudience()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each combination must register against its own registry so
			// the JWKS collector does not collide across subtests.
			opts := append([]Option{WithRegisterer(prometheus.NewRegistry())}, tc.opts...)
			jm := initModule(t, opts)

			require.NotNil(t, jm.provider, "Init must construct a provider")

			var infra app.Infrastructure
			jm.Populate(&infra)
			assert.Same(t, jm.provider, Provider(infra),
				"Populate must publish the constructed provider under ResourceProviderKey")
		})
	}
}

func TestInit_RegistersJWKSCollectorOnSuppliedRegisterer(t *testing.T) {
	reg := prometheus.NewRegistry()
	initModule(t, []Option{
		WithIssuer("https://idp.example.com"),
		WithAudience("backend"),
		WithRegisterer(reg),
	})

	mfs, err := reg.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "jwks_last_successful_fetch_timestamp_seconds" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"Init must register the JWKS metrics collector on the supplied registerer")
}

func TestInit_PopulateBeforeInitPublishesNothing(t *testing.T) {
	mod := Module(testJWKS, WithoutIssuer(), WithoutAudience())
	var infra app.Infrastructure
	mod.Populate(&infra)
	assert.Nil(t, Provider(infra),
		"Populate before Init must not publish a nil provider as a resource")
}
