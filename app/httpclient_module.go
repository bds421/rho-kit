package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// HTTPClientProvider is the public capability interface published
// by the built-in httpclient module. Bridge modules in app/* that
// need the kit-configured *http.Client at Init time (e.g., app/jwt
// uses it to fetch the JWKS document) look it up via
// `mc.Module(HTTPClientModuleName).(HTTPClientProvider).Client()`.
//
// The httpclient builtin is initialised after any user
// [TracingProvider] modules and before all other user modules, so
// non-tracing bridges always see a ready client at Init. Modules that
// implement TracingProvider run before httpclient and must not depend
// on it during their own Init.
type HTTPClientProvider interface {
	Client() *http.Client
}

// httpClientModule implements the Module interface for creating an HTTP client.
// It reads the tracing state from a registered [TracingProvider] (when present)
// to decide whether to create a tracing-instrumented or plain HTTP client.
type httpClientModule struct {
	BaseModule

	// tracingConfigured indicates that a tracing module was registered.
	// When true, Init looks up the registered [TracingProvider] and asks
	// whether tracing initialized successfully.
	tracingConfigured bool

	// timeout is the outbound client timeout. Zero means 10s.
	timeout time.Duration

	// initialized during Init
	client *http.Client
}

// newHTTPClientModule creates an HTTP client module.
// tracingConfigured should be true when a tracing module is registered
// (so the httpClient module can query its TracingActive state during Init).
func newHTTPClientModule(tracingConfigured bool, timeout time.Duration) *httpClientModule {
	return &httpClientModule{
		BaseModule:        NewBaseModule(HTTPClientModuleName),
		tracingConfigured: tracingConfigured,
		timeout:           timeout,
	}
}

func (m *httpClientModule) Init(_ context.Context, mc ModuleContext) error {
	var (
		cTLS *tls.Config
		err  error
	)
	if mc.TLSCertSource != nil {
		// Hot-rotation path: share the same source the public server
		// uses so cert/key/CA reloads flow through the default
		// outbound client without restart.
		cTLS = netutil.ReloadingClientTLS(mc.TLSCertSource)
	} else {
		cTLS, err = mc.Config.TLS.ClientTLS()
		if err != nil {
			return fmt.Errorf("httpclient module: build client TLS: %w", err)
		}
	}

	tracingActive := false
	if m.tracingConfigured {
		// Exactly one TracingProvider is allowed — map iteration order
		// would otherwise make TracingActive nondeterministic.
		var found TracingProvider
		for _, mod := range mc.modules {
			tp, ok := mod.(TracingProvider)
			if !ok {
				continue
			}
			if found != nil {
				return fmt.Errorf("httpclient module: multiple TracingProvider modules registered; only one is allowed")
			}
			found = tp
		}
		if found != nil {
			tracingActive = found.TracingActive()
		}
	}

	timeout := m.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if tracingActive {
		m.client = httpx.NewTracingHTTPClient(timeout, cTLS)
	} else {
		m.client = httpx.NewHTTPClient(timeout, cTLS)
	}

	mc.Logger.Info("http client configured", "tracing", tracingActive)
	return nil
}

func (m *httpClientModule) Populate(infra *Infrastructure) {
	if m.client != nil {
		infra.SetResource(ResourceHTTPClientKey, m.client)
	}
}

// Client returns the initialized HTTP client, or nil if Init has not been called.
func (m *httpClientModule) Client() *http.Client {
	return m.client
}
