package app

import (
	"net/http"
	"os"

	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// stubHTTPConfig is a test helper that implements
// [HTTPConfigProvider] without importing app/http (which would
// create a cycle — app/http imports app/v2 for the Module
// interface). Internal app/v2 tests use it to drive
// HTTP-server-related test scenarios after the legacy Builder
// methods (WithoutTLS, AllowInternalNonLoopback, etc.) moved
// into app/http.Module() options in wave 94.
type stubHTTPConfig struct {
	BaseModule
	plaintext            bool
	optClientCerts       bool
	internalNonLoopback  bool
	reloadingTLSActive   bool
	reloadingTLSOpts     []netutil.FilesCertificateSourceOption
	tlsReloadSignals     []os.Signal
	disableDefaultStack  bool
	stackOptionList      []stack.Option
	serverOptionList     []httpx.ServerOption
	customReadinessImpl  http.Handler
}

// HTTPConfigProvider implementation.
func (s *stubHTTPConfig) AllowPlaintext() bool           { return s.plaintext }
func (s *stubHTTPConfig) OptionalClientCerts() bool      { return s.optClientCerts }
func (s *stubHTTPConfig) AllowInternalNonLoopback() bool { return s.internalNonLoopback }
func (s *stubHTTPConfig) ReloadingTLSOptions() ([]netutil.FilesCertificateSourceOption, bool) {
	return s.reloadingTLSOpts, s.reloadingTLSActive
}
func (s *stubHTTPConfig) TLSReloadSignals() []os.Signal       { return s.tlsReloadSignals }
func (s *stubHTTPConfig) DisableDefaultStack() bool           { return s.disableDefaultStack }
func (s *stubHTTPConfig) StackOptions() []stack.Option        { return s.stackOptionList }
func (s *stubHTTPConfig) ServerOptions() []httpx.ServerOption { return s.serverOptionList }
func (s *stubHTTPConfig) CustomReadiness() http.Handler       { return s.customReadinessImpl }

// allowPlaintextOnly is the most common test-fixture shape: the
// service has no real TLS configured and needs the validator to
// pass without enforcing it.
func allowPlaintextOnly() *stubHTTPConfig {
	return &stubHTTPConfig{
		BaseModule: NewBaseModule("test-http-plaintext"),
		plaintext:  true,
	}
}
