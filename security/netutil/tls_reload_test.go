package netutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateRotatableTLSFixture builds a tls fixture in a private temp
// dir and returns the TLSConfig plus a rotate() callback that
// re-issues fresh cert + CA material into the same paths so a
// reload-capable consumer can pick them up. Used by the H-009
// regression tests.
func generateRotatableTLSFixture(t *testing.T) (TLSConfig, func(t *testing.T)) {
	t.Helper()
	dir := t.TempDir()
	cfg := TLSConfig{
		CACert: filepath.Join(dir, "ca.pem"),
		Cert:   filepath.Join(dir, "cert.pem"),
		Key:    filepath.Join(dir, "key.pem"),
	}
	writeFreshCert(t, cfg)
	rotate := func(t *testing.T) {
		t.Helper()
		writeFreshCert(t, cfg)
	}
	return cfg, rotate
}

func writeFreshCert(t *testing.T, cfg TLSConfig) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "rotatable-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	writePEMForTest(t, cfg.CACert, "CERTIFICATE", der)
	writePEMForTest(t, cfg.Cert, "CERTIFICATE", der)
	writePEMForTest(t, cfg.Key, "EC PRIVATE KEY", keyDER)
}

func TestFilesCertificateSource_LoadAndReload(t *testing.T) {
	t.Parallel()
	cfg, rotate := generateRotatableTLSFixture(t)

	src, err := NewFilesCertificateSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	first, err := src.ServerCertificate()
	require.NoError(t, err)
	require.NotNil(t, first)
	firstSerial := readSerial(t, first)

	// Rotate cert files in-place and reload.
	rotate(t)
	require.NoError(t, src.Reload())

	second, err := src.ServerCertificate()
	require.NoError(t, err)
	secondSerial := readSerial(t, second)

	assert.NotEqual(t, firstSerial, secondSerial,
		"after Reload, ServerCertificate must return the freshly issued cert (different serial)")
	assert.Equal(t, uint64(2), src.Reloads())
	assert.Equal(t, uint64(0), src.ReloadErrors())
}

func TestFilesCertificateSource_ReloadFailureKeepsLastGood(t *testing.T) {
	t.Parallel()
	cfg, _ := generateRotatableTLSFixture(t)

	src, err := NewFilesCertificateSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	preSerial := readSerial(t, mustServerCert(t, src))

	// Corrupt the cert file mid-rotation.
	require.NoError(t, os.WriteFile(cfg.Cert, []byte("not a pem"), 0o600))
	err = src.Reload()
	require.Error(t, err)
	assert.Equal(t, uint64(1), src.ReloadErrors())

	// Read must still return the previous good cert.
	postSerial := readSerial(t, mustServerCert(t, src))
	assert.Equal(t, preSerial, postSerial,
		"reload error must keep the previous good snapshot in place")
}

func TestFilesCertificateSource_PollingPicksUpRotation(t *testing.T) {
	t.Parallel()
	cfg, rotate := generateRotatableTLSFixture(t)

	src, err := NewFilesCertificateSource(cfg, WithReloadInterval(time.Second))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	firstSerial := readSerial(t, mustServerCert(t, src))
	initialReloads := src.Reloads()

	rotate(t)

	// Wait for the polling goroutine to pick up the rotation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, src.WaitForReload(ctx, initialReloads))

	secondSerial := readSerial(t, mustServerCert(t, src))
	assert.NotEqual(t, firstSerial, secondSerial,
		"polling reload must observe the rotated cert without an explicit Reload call")
}

func TestNewFilesCertificateSource_RejectsPartialConfig(t *testing.T) {
	t.Parallel()
	_, err := NewFilesCertificateSource(TLSConfig{Cert: "/dev/null"})
	require.Error(t, err)
}

func TestReloadingServerTLS_GetConfigForClientReturnsCurrentPool(t *testing.T) {
	t.Parallel()
	cfg, _ := generateRotatableTLSFixture(t)
	src, err := NewFilesCertificateSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	tlsCfg := ReloadingServerTLS(src)
	require.NotNil(t, tlsCfg.GetCertificate)
	require.NotNil(t, tlsCfg.GetConfigForClient)
	require.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MinVersion)
	require.Equal(t, tls.RequireAndVerifyClientCert, tlsCfg.ClientAuth)

	// GetConfigForClient must return a fresh Config with the current
	// cert + CA pool populated.
	subCfg, err := tlsCfg.GetConfigForClient(nil)
	require.NoError(t, err)
	require.NotNil(t, subCfg)
	require.NotNil(t, subCfg.ClientCAs)
	require.Len(t, subCfg.Certificates, 1)
}

func TestReloadingClientTLS_UsesGetClientCertificate(t *testing.T) {
	t.Parallel()
	cfg, _ := generateRotatableTLSFixture(t)
	src, err := NewFilesCertificateSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	tlsCfg := ReloadingClientTLS(src)
	require.NotNil(t, tlsCfg.GetClientCertificate)
	require.NotNil(t, tlsCfg.VerifyConnection)
	require.True(t, tlsCfg.InsecureSkipVerify,
		"hot-rotating clients must bypass tls.Config's static RootCAs and verify via VerifyConnection — the kit's pool is the source of truth")
	require.Equal(t, uint16(tls.VersionTLS13), tlsCfg.MinVersion)

	leaf, err := tlsCfg.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)
	require.NotNil(t, leaf)
}

func TestReloadingServerTLS_PanicsOnNilSource(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		ReloadingServerTLS(nil)
	})
}

func TestReloadingClientTLS_PanicsOnNilSource(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		ReloadingClientTLS(nil)
	})
}

func mustServerCert(t *testing.T, s *FilesCertificateSource) *tls.Certificate {
	t.Helper()
	c, err := s.ServerCertificate()
	require.NoError(t, err)
	return c
}

func readSerial(t *testing.T, c *tls.Certificate) string {
	t.Helper()
	require.NotEmpty(t, c.Certificate)
	parsed, err := x509.ParseCertificate(c.Certificate[0])
	require.NoError(t, err)
	return parsed.SerialNumber.String()
}
