package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
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

// LogValue implements slog.LogValuer to avoid logging mounted secret paths.
func (c TLSConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled()),
		slog.Bool("ca_cert_configured", c.CACert != ""),
		slog.Bool("cert_configured", c.Cert != ""),
		slog.Bool("key_configured", c.Key != ""),
	)
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
			return fmt.Errorf("%s: file not accessible", entry.name)
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
// This is the default. The option remains useful at call sites that want to
// make the mTLS requirement explicit.
func WithRequireClientCert() ServerTLSOption {
	return func(o *serverTLSOpts) { o.requireClientCert = true }
}

// WithOptionalClientCert downgrades server-side TLS from mutual TLS to
// verify-if-present client certificates. Use only for listeners behind a
// trusted TLS terminator that re-encrypts to the service but cannot present
// a client certificate.
func WithOptionalClientCert() ServerTLSOption {
	return func(o *serverTLSOpts) { o.requireClientCert = false }
}

// ServerTLS returns a *tls.Config for an HTTP server with mTLS support.
//
// By default, client certificates are required and verified
// (tls.RequireAndVerifyClientCert). This matches the kit convention that
// setting TLS_CA_CERT, TLS_CERT, and TLS_KEY enables mTLS globally.
//
// Use [WithOptionalClientCert] only for gateway-fronted services that must
// accept anonymous connections from a trusted TLS terminator.
//
// Note: callers using [github.com/bds421/rho-kit/app] do not need to pass
// options directly — the Builder exposes [Builder.WithOptionalClientCertificates]
// for gateway-fronted services (audit FR-014).
//
// Returns nil if TLS is not enabled.
func (c TLSConfig) ServerTLS(opts ...ServerTLSOption) (*tls.Config, error) {
	o := serverTLSOpts{requireClientCert: true}
	for _, opt := range opts {
		if opt == nil {
			panic("netutil: ServerTLS option must not be nil")
		}
		opt(&o)
	}
	if !c.Enabled() {
		return nil, nil
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
		return tls.Certificate{}, nil, fmt.Errorf("load cert/key pair failed")
	}

	caPEM, err := os.ReadFile(c.CACert)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read CA cert failed")
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("failed to parse CA certificate")
	}

	return cert, caPool, nil
}

// Reloading constructs a [FilesCertificateSource] from this
// TLSConfig. Use this when a service needs hot rotation of
// cert/key/CA files (Kubernetes secret rotation, Vault Agent
// templating) without restarting the process.
//
// The result is a single CertificateSource that backs both server
// and client TLS via [ReloadingServerTLS] and [ReloadingClientTLS].
// Pass [WithReloadInterval] to enable polling, or call
// [FilesCertificateSource.Reload] from a SIGHUP handler.
//
// Returns an error if TLS is not fully configured — call sites
// that may run without TLS should branch on [TLSConfig.Enabled]
// first.
func (c TLSConfig) Reloading(opts ...FilesCertificateSourceOption) (*FilesCertificateSource, error) {
	return NewFilesCertificateSource(c, opts...)
}

// LoadTLS reads TLS config from standard environment variables.
func LoadTLS() TLSConfig {
	return TLSConfig{
		CACert: config.Get("TLS_CA_CERT", ""),
		Cert:   config.Get("TLS_CERT", ""),
		Key:    config.Get("TLS_KEY", ""),
	}
}
