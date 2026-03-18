package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/core/config"
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
// Returns nil if TLS is not enabled.
func (c TLSConfig) Validate() error {
	if !c.Enabled() {
		return nil
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

// ServerTLS returns a *tls.Config for an HTTP server that verifies
// client certificates when presented (mTLS). Clients that do not present
// a certificate are still allowed — this enables trusted gateways like
// Oathkeeper to proxy requests while service-to-service calls use mutual auth.
// Returns nil if TLS is not enabled.
func (c TLSConfig) ServerTLS() (*tls.Config, error) {
	if !c.Enabled() {
		return nil, nil
	}

	cert, caPool, err := c.loadCertAndCA()
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
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
