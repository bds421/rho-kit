package http_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	apphttp "github.com/bds421/rho-kit/app/http/v2"
)

// issue makes a certificate for usage, signed by ca (self-signed when ca
// is nil).
func issue(t *testing.T, ca *tls.Certificate, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "svc"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"svc"}, ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	parent, signer := tmpl, any(key)
	if ca == nil {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign
	} else {
		parent, signer = ca.Leaf, ca.PrivateKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// The internal ops server takes a TLS configuration of its own: a client
// without the certificate it requires reads nothing, one with it reads
// /metrics, while the service has no kit TLS at all.
func TestTheInternalServerUsesItsOwnTLS(t *testing.T) {
	t.Chdir(t.TempDir())
	internal, err := net.Listen("unix", "ops.sock")
	if err != nil {
		t.Fatal(err)
	}
	ca := issue(t, nil, x509.ExtKeyUsageAny)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Leaf)
	server, client := issue(t, &ca, x509.ExtKeyUsageServerAuth), issue(t, &ca, x509.ExtKeyUsageClientAuth)
	b := app.New("svc", "v0.0.1", app.BaseConfig{}).
		WithoutPublicServer().
		With(apphttp.Module(apphttp.WithoutTLS(), apphttp.WithInternalListener(internal),
			apphttp.WithInternalTLS(&tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13})))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.RunContext(ctx) }()

	get := func(certs []tls.Certificate) (int, error) {
		c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "unix", "ops.sock") },
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "svc", Certificates: certs, MinVersion: tls.VersionTLS13},
		}}
		resp, err := c.Get("https://svc/metrics")
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	// The listener is bound already, so the handshake waits for the server.
	code, err := get([]tls.Certificate{client})
	if err != nil || code != http.StatusOK {
		t.Fatalf("with the client certificate: %d %v", code, err)
	}
	if _, err := get(nil); err == nil {
		t.Fatal("a client without a certificate read /metrics")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}
