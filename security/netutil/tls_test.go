package netutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

var _ slog.LogValuer = TLSConfig{}

func TestTLSConfigLogValueRedactsSecretPaths(t *testing.T) {
	cfg := TLSConfig{
		CACert: "/var/run/secrets/tls/ca.pem",
		Cert:   "/var/run/secrets/tls/cert.pem",
		Key:    "/var/run/secrets/tls/key.pem",
	}

	rendered := cfg.LogValue().String()

	for _, path := range []string{cfg.CACert, cfg.Cert, cfg.Key} {
		if strings.Contains(rendered, path) {
			t.Fatalf("LogValue leaked TLS path %q in %q", path, rendered)
		}
	}
	for _, expected := range []string{"enabled=true", "key_configured=true"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestTLSConfigValidate_DoesNotReflectSecretPaths(t *testing.T) {
	cfg := TLSConfig{
		CACert: "/missing/secret-token/ca.pem",
		Cert:   "/missing/secret-token/cert.pem",
		Key:    "/missing/secret-token/key.pem",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected inaccessible TLS file error")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), cfg.CACert) {
		t.Fatalf("Validate leaked TLS path: %v", err)
	}
}

func TestServerTLS_DoesNotReflectSecretPaths(t *testing.T) {
	cfg := TLSConfig{
		CACert: "/missing/secret-token/ca.pem",
		Cert:   "/missing/secret-token/cert.pem",
		Key:    "/missing/secret-token/key.pem",
	}

	_, err := cfg.ServerTLS()
	if err == nil {
		t.Fatal("expected TLS load error")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), cfg.Cert) || strings.Contains(err.Error(), cfg.Key) {
		t.Fatalf("ServerTLS leaked TLS path: %v", err)
	}
}

func TestServerTLS_DisabledReturnsNil(t *testing.T) {
	cfg, err := TLSConfig{}.ServerTLS(WithRequireClientCert())
	if err != nil {
		t.Fatalf("ServerTLS returned error for disabled TLS: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil TLS config when TLS is disabled")
	}
}

func TestServerTLS_DefaultRequiresClientCert(t *testing.T) {
	cfg := realTLSConfigForTest(t)

	got, err := cfg.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	if got == nil {
		t.Fatal("expected TLS config")
	}
	if got.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", got.ClientAuth)
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", got.MinVersion)
	}
}

func TestServerTLS_WithOptionalClientCertDowngradesToVerifyIfGiven(t *testing.T) {
	cfg := realTLSConfigForTest(t)

	got, err := cfg.ServerTLS(WithOptionalClientCert())
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	if got == nil {
		t.Fatal("expected TLS config")
	}
	if got.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("ClientAuth = %v, want VerifyClientCertIfGiven", got.ClientAuth)
	}
}

func TestServerTLS_WithRequireClientCertIsExplicitDefault(t *testing.T) {
	cfg := realTLSConfigForTest(t)

	got, err := cfg.ServerTLS(WithRequireClientCert())
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	if got.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", got.ClientAuth)
	}
}

func TestServerTLS_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil ServerTLS option")
		}
	}()
	_, _ = TLSConfig{}.ServerTLS(nil)
}

func realTLSConfigForTest(t *testing.T) TLSConfig {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	caPath := dir + "/ca.pem"
	certPath := dir + "/cert.pem"
	keyPath := dir + "/key.pem"
	writePEMForTest(t, caPath, "CERTIFICATE", der)
	writePEMForTest(t, certPath, "CERTIFICATE", der)
	writePEMForTest(t, keyPath, "EC PRIVATE KEY", keyDER)
	return TLSConfig{CACert: caPath, Cert: certPath, Key: keyPath}
}

func writePEMForTest(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
