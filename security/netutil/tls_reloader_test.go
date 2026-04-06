package netutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generateSelfSignedCert creates a self-signed cert+key in the given directory.
func generateSelfSignedCert(t *testing.T, dir, cn string) (certPath, keyPath, caPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	caPath = filepath.Join(dir, "ca.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, os.WriteFile(certPath, certPEM, 0600))
	require.NoError(t, os.WriteFile(caPath, certPEM, 0600)) // self-signed = own CA

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0600))

	return certPath, keyPath, caPath
}

func TestTLSReloader_Disabled(t *testing.T) {
	r, err := NewTLSReloader(TLSConfig{}, nil)
	assert.NoError(t, err)
	assert.Nil(t, r)
}

func TestTLSReloader_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caPath := generateSelfSignedCert(t, dir, "test-initial")

	r, err := NewTLSReloader(TLSConfig{
		CACert: caPath,
		Cert:   certPath,
		Key:    keyPath,
	}, nil)
	require.NoError(t, err)

	serverTLS := r.ServerTLS()
	assert.NotNil(t, serverTLS.GetCertificate)

	clientTLS := r.ClientTLS()
	assert.NotNil(t, clientTLS.GetClientCertificate)

	// Verify the certificate can be retrieved.
	cert, err := clientTLS.GetClientCertificate(nil)
	require.NoError(t, err)
	assert.NotNil(t, cert)
}

func TestTLSReloader_DetectsRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caPath := generateSelfSignedCert(t, dir, "test-v1")

	r, err := NewTLSReloader(TLSConfig{
		CACert: caPath,
		Cert:   certPath,
		Key:    keyPath,
	}, nil)
	require.NoError(t, err)

	// Get initial cert serial.
	cert1, _ := r.ClientTLS().GetClientCertificate(nil)
	parsed1, _ := x509.ParseCertificate(cert1.Certificate[0])
	serial1 := parsed1.SerialNumber

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Start(ctx) }()
	time.Sleep(100 * time.Millisecond) // let watcher initialize

	// Generate new cert (different serial) and overwrite.
	generateSelfSignedCert(t, dir, "test-v2")

	// Wait for reload.
	assert.Eventually(t, func() bool {
		cert2, _ := r.ClientTLS().GetClientCertificate(nil)
		parsed2, _ := x509.ParseCertificate(cert2.Certificate[0])
		return parsed2.SerialNumber.Cmp(serial1) != 0
	}, 2*time.Second, 50*time.Millisecond, "certificate should have been rotated")
}

func TestTLSReloader_BadCertKeepsOld(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caPath := generateSelfSignedCert(t, dir, "test-keep")

	r, err := NewTLSReloader(TLSConfig{
		CACert: caPath,
		Cert:   certPath,
		Key:    keyPath,
	}, nil)
	require.NoError(t, err)

	cert1, _ := r.ClientTLS().GetClientCertificate(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	// Write invalid cert content.
	require.NoError(t, os.WriteFile(certPath, []byte("not-a-cert"), 0600))
	time.Sleep(500 * time.Millisecond)

	// Old cert should still be served.
	cert2, _ := r.ClientTLS().GetClientCertificate(nil)
	assert.Equal(t, cert1.Certificate[0], cert2.Certificate[0])
}
