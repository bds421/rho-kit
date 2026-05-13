package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/httpx/v2"
)

// httpClientModule implements the Module interface for creating an HTTP client.
// It reads the tracing state from a registered [TracingProvider] (when present)
// to decide whether to create a tracing-instrumented or plain HTTP client.
type httpClientModule struct {
	BaseModule

	// tracingConfigured indicates that a tracing module was registered.
	// When true, Init looks up the registered [TracingProvider] and asks
	// whether tracing initialized successfully.
	tracingConfigured bool

	// initialized during Init
	client    *http.Client
	clientTLS *tls.Config
}

// newHTTPClientModule creates an HTTP client module.
// tracingConfigured should be true when a tracing module is registered
// (so the httpClient module can query its TracingActive state during Init).
func newHTTPClientModule(tracingConfigured bool) *httpClientModule {
	return &httpClientModule{
		BaseModule:        NewBaseModule("httpclient"),
		tracingConfigured: tracingConfigured,
	}
}

func (m *httpClientModule) Init(_ context.Context, mc ModuleContext) error {
	cTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("httpclient module: build client TLS: %w", err)
	}
	m.clientTLS = cTLS

	tracingActive := false
	if m.tracingConfigured {
		// Find the tracing provider module by interface satisfaction.
		// app/tracing.Module() returns a Module whose value implements
		// TracingProvider; app/v2 does not import OTel directly.
		for _, mod := range mc.modules {
			if tp, ok := mod.(TracingProvider); ok {
				tracingActive = tp.TracingActive()
				break
			}
		}
	}

	if tracingActive {
		m.client = httpx.NewTracingHTTPClient(10*time.Second, cTLS)
	} else {
		m.client = httpx.NewHTTPClient(10*time.Second, cTLS)
	}

	mc.Logger.Info("http client configured", "tracing", tracingActive)
	return nil
}

func (m *httpClientModule) Populate(infra *Infrastructure) {
	infra.HTTPClient = m.client
	infra.ClientTLS = m.clientTLS
}

// Client returns the initialized HTTP client, or nil if Init has not been called.
func (m *httpClientModule) Client() *http.Client {
	return m.client
}
