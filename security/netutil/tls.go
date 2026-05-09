package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/core/v2/config"
)

// TLSConfig holds paths to TLS certificate files for mTLS.
// When all three paths are set, TLS is enabled.
// When any path is empty, TLS is disabled (development mode).
type TLSConfig struct {
	CACert string // Path to CA certificate (PEM)
	Cert   string // Path to client/server certificate (PEM)
	Key    string // Path to private key (PEM)
}

// Enabled returns true when all TLS paths are configured.
func (c TLSConfig) Enabled() bool {
	return c.CACert != "" && c.Cert != "" && c.Key != ""
}

// Validate checks that the configured TLS files exist and are readable.
// Returns nil if TLS is fully disabled (all three paths empty).
//
// FR-015 [MED]: a partial configuration (one or two of the three
// paths set) used to validate as disabled, so a typo silently ran
// the service plaintext. Now Validate rejects partial configs with
// a typed error so the wiring bug surfaces at startup. Use
// [TLSConfig.Enabled] when callers genuinely want the "all-or-
// nothing" boolean for branching.
func (c TLSConfig) Validate() error {
	set, missing := tlsConfigStatus(c)
	if len(set) == 0 {
		// Fully disabled — caller's choice; the [Builder] enforces TLS
		// or [Builder.WithoutTLS] elsewhere.
		return nil
	}
	if len(missing) > 0 {
		return fmt.Errorf("netutil: partial TLS configuration — set fields %v but missing %v; either set all of TLS_CA_CERT, TLS_CERT, TLS_KEY or none", set, missing)
	}
	for _, entry := range []struct{ name, path string }{
		{"TLS_CA_CERT", c.CACert},
		{"TLS_CERT", c.Cert},
		{"TLS_KEY", c.Key},
	} {
		f, err := os.Open(entry.path)
		if err != nil {
			return fmt.Errorf("%s: file not accessible: %w", entry.name, err)
		}
		_ = f.Close()
	}
	return nil
}

// tlsConfigStatus returns the names of populated and missing TLS
// fields. Used by Validate to distinguish "fully off" from "partially
// on".
func tlsConfigStatus(c TLSConfig) (set []string, missing []string) {
	for _, entry := range []struct{ name, path string }{
		{"TLS_CA_CERT", c.CACert},
		{"TLS_CERT", c.Cert},
		{"TLS_KEY", c.Key},
	} {
		if entry.path != "" {
			set = append(set, entry.name)
		} else {
			missing = append(missing, entry.name)
		}
	}
	return set, missing
}

// ServerTLSOption configures server-side TLS behavior.
type ServerTLSOption func(*serverTLSOpts)

type serverTLSOpts struct {
	requireClientCert bool
}

// WithRequireClientCert enforces that all clients present a valid certificate.
// Without this option, client certificates are verified if presented but not
// required (tls.VerifyClientCertIfGiven). Use this when all clients are known
// to have certificates — e.g., internal service mesh without a gateway.
func WithRequireClientCert() ServerTLSOption {
	return func(o *serverTLSOpts) { o.requireClientCert = true }
}

// ServerTLS returns a *tls.Config for an HTTP server with mTLS support.
//
// By default, client certificates are verified when presented but not required
// (tls.VerifyClientCertIfGiven). This enables trusted gateways like Oathkeeper
// to proxy requests without certificates while service-to-service calls use
// mutual auth.
//
// Use [WithRequireClientCert] to enforce that ALL clients present a valid
// certificate (tls.RequireAndVerifyClientCert).
//
// Note: callers using [github.com/bds421/rho-kit/app] do not need to pass
// this option directly — the Builder enables [WithRequireClientCert] by
// default and exposes [Builder.WithOptionalClientCertificates] for
// gateway-fronted services (audit FR-014).
//
// Returns nil if TLS is not enabled.
func (c TLSConfig) ServerTLS(opts ...ServerTLSOption) (*tls.Config, error) {
	if !c.Enabled() {
		return nil, nil
	}

	var o serverTLSOpts
	for _, opt := range opts {
		opt(&o)
	}

	cert, caPool, err := c.loadCertAndCA()
	if err != nil {
		return nil, err
	}

	clientAuth := tls.VerifyClientCertIfGiven
	if o.requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   clientAuth,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLS returns a *tls.Config for an HTTP/AMQP/SQL client that
// presents a client certificate and verifies the server against the CA.
// Returns nil if TLS is not enabled.
func (c TLSConfig) ClientTLS() (*tls.Config, error) {
	if !c.Enabled() {
		return nil, nil
	}

	cert, caPool, err := c.loadCertAndCA()
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func (c TLSConfig) loadCertAndCA() (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load cert/key pair: %w", err)
	}

	caPEM, err := os.ReadFile(c.CACert)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read CA cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("failed to parse CA certificate")
	}

	return cert, caPool, nil
}

// LoadTLS reads TLS config from standard environment variables.
func LoadTLS() TLSConfig {
	return TLSConfig{
		CACert: config.Get("TLS_CA_CERT", ""),
		Cert:   config.Get("TLS_CERT", ""),
		Key:    config.Get("TLS_KEY", ""),
	}
}
