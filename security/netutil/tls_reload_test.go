package netutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
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
	require.ErrorIs(t, err, ErrTLSCertKeyPairLoad)
	assert.Equal(t, "cert_key_pair_load", TLSLoadErrorReason(err))
	assert.NotContains(t, err.Error(), cfg.Cert)
	assert.NotContains(t, err.Error(), cfg.Key)
	assert.Equal(t, uint64(1), src.ReloadErrors())

	// Read must still return the previous good cert.
	postSerial := readSerial(t, mustServerCert(t, src))
	assert.Equal(t, preSerial, postSerial,
		"reload error must keep the previous good snapshot in place")
}

func TestFilesCertificateSource_PollingPicksUpRotation(t *testing.T) {
	t.Parallel()
	cfg, rotate := generateRotatableTLSFixture(t)

	src, err := NewFilesCertificateSource(cfg, withTestReloadInterval(20*time.Millisecond))
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

func TestWithReloadInterval_ClampsSubSecondPolling(t *testing.T) {
	t.Parallel()

	var opts filesCertificateSourceOpts
	WithReloadInterval(time.Millisecond)(&opts)
	require.Equal(t, time.Second, opts.pollEvery)
}

// withTestReloadInterval bypasses the public one-second safety clamp so the
// polling behavior can be exercised without making the unit suite sleep for a
// production-scale interval. TestWithReloadInterval_ClampsSubSecondPolling
// separately pins the public contract.
func withTestReloadInterval(d time.Duration) FilesCertificateSourceOption {
	return func(o *filesCertificateSourceOpts) { o.pollEvery = d }
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

// TestReloadingClientTLS_FailsClosedWithoutServerName pins R2-002:
// the verification callback must refuse the handshake when ServerName
// is empty. Without this guard, InsecureSkipVerify=true combined with
// VerifyConnection would let the peer's chain pass without hostname
// binding — strictly weaker than the stock Go TLS client.
func TestReloadingClientTLS_FailsClosedWithoutServerName(t *testing.T) {
	t.Parallel()
	cfg, _ := generateRotatableTLSFixture(t)
	src, err := NewFilesCertificateSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	tlsCfg := ReloadingClientTLS(src)
	require.NotNil(t, tlsCfg.VerifyConnection)

	err = tlsCfg.VerifyConnection(tls.ConnectionState{
		ServerName: "", // SDK that did not stamp the hostname
	})
	require.ErrorIs(t, err, ErrServerNameRequired)
}

// poolSource is a CertificateSource whose only meaningful method is
// CAs(); ReloadingClientTLS.VerifyConnection consults only the trust
// pool, so the cert getters fail loudly if the test path ever calls
// them by accident.
type poolSource struct{ pool *x509.CertPool }

func (p poolSource) ServerCertificate() (*tls.Certificate, error) {
	return nil, errors.New("poolSource: ServerCertificate not expected")
}

func (p poolSource) ClientCertificate() (*tls.Certificate, error) {
	return nil, errors.New("poolSource: ClientCertificate not expected")
}

func (p poolSource) CAs() (*x509.CertPool, error) { return p.pool, nil }

// testCA is a self-signed CA plus the material needed to sign leaves.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T, commonName string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return testCA{cert: cert, key: key, pool: pool}
}

// signLeaf issues a server leaf cert signed by ca for the given DNS SAN.
func (ca testCA) signLeaf(t *testing.T, dnsName string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{dnsName},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return leaf
}

// TestReloadingClientTLS_VerifyConnectionChain pins the core mTLS
// server-cert verification path. Because ReloadingClientTLS sets
// InsecureSkipVerify=true and replaces verification with
// VerifyConnection, this callback is the only thing standing between a
// client and an unauthenticated peer. A regression that turned the
// chain Verify into a no-op (e.g. dropping the err check) would pass the
// other tests while silently disabling peer authentication, so we assert
// both the positive and the negative paths explicitly here.
func TestReloadingClientTLS_VerifyConnectionChain(t *testing.T) {
	t.Parallel()

	ca := newTestCA(t, "trusted-ca")
	foreign := newTestCA(t, "foreign-ca")

	goodLeaf := ca.signLeaf(t, "good.example")         // signed by trusted CA
	foreignLeaf := foreign.signLeaf(t, "good.example") // right name, wrong CA

	tlsCfg := ReloadingClientTLS(poolSource{pool: ca.pool})
	require.NotNil(t, tlsCfg.VerifyConnection)

	t.Run("accepts chain signed by trusted CA with matching hostname", func(t *testing.T) {
		err := tlsCfg.VerifyConnection(tls.ConnectionState{
			ServerName:       "good.example",
			PeerCertificates: []*x509.Certificate{goodLeaf},
		})
		require.NoError(t, err,
			"a leaf signed by the source's trusted CA with a matching SAN must verify")
	})

	t.Run("rejects chain from an untrusted CA", func(t *testing.T) {
		err := tlsCfg.VerifyConnection(tls.ConnectionState{
			ServerName:       "good.example",
			PeerCertificates: []*x509.Certificate{foreignLeaf},
		})
		require.Error(t, err,
			"a leaf signed by a CA not in the trust pool must be rejected")
	})

	t.Run("rejects hostname mismatch even with a trusted CA", func(t *testing.T) {
		err := tlsCfg.VerifyConnection(tls.ConnectionState{
			ServerName:       "evil.example",
			PeerCertificates: []*x509.Certificate{goodLeaf},
		})
		require.Error(t, err,
			"a trusted-CA leaf whose SAN does not match ServerName must be rejected")
	})

	t.Run("rejects when peer presents no certificates", func(t *testing.T) {
		err := tlsCfg.VerifyConnection(tls.ConnectionState{
			ServerName:       "good.example",
			PeerCertificates: nil,
		})
		require.Error(t, err,
			"a handshake with no peer certificates must be rejected")
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
