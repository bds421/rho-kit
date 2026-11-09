package health

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return testCA{cert: cert, key: key, pool: pool}
}

func (ca testCA) issue(t *testing.T, name string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func port(srv *httptest.Server) int { return srv.Listener.Addr().(*net.TCPAddr).Port }

func TestCheckHealthPlainHTTP(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	opts := HealthCheckOptions{Host: "127.0.0.1", Port: port(srv)}
	if err := CheckHealth(context.Background(), opts); err != nil {
		t.Fatalf("ready endpoint: %v", err)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := CheckHealth(context.Background(), opts); err == nil {
		t.Fatal("a 503 passed")
	}
}

func TestCheckHealthRefusesRedirects(t *testing.T) {
	srv := httptest.NewServer(http.RedirectHandler("/elsewhere", http.StatusFound))
	defer srv.Close()
	if err := CheckHealth(context.Background(), HealthCheckOptions{Host: "127.0.0.1", Port: port(srv)}); err == nil {
		t.Fatal("a redirect passed")
	}
}

func TestCheckHealthPresentsClientCertificate(t *testing.T) {
	ca := newTestCA(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "role.example.invalid", x509.ExtKeyUsageServerAuth)},
		ClientCAs:    ca.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	defer srv.Close()
	base := HealthCheckOptions{Host: "127.0.0.1", Port: port(srv)}

	withCert := base
	withCert.TLS = &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "probe.example.invalid", x509.ExtKeyUsageClientAuth)},
		RootCAs:      ca.pool,
		ServerName:   "role.example.invalid",
		MinVersion:   tls.VersionTLS12,
	}
	if err := CheckHealth(context.Background(), withCert); err != nil {
		t.Fatalf("with the client certificate: %v", err)
	}

	noCert := base
	noCert.TLS = &tls.Config{RootCAs: ca.pool, ServerName: "role.example.invalid", MinVersion: tls.VersionTLS12}
	if err := CheckHealth(context.Background(), noCert); err == nil {
		t.Fatal("passed without a client certificate")
	}

	wrongName := withCert
	wrongName.TLS = withCert.TLS.Clone()
	wrongName.TLS.ServerName = "other.example.invalid"
	if err := CheckHealth(context.Background(), wrongName); err == nil {
		t.Fatal("passed with a server certificate for another name")
	}

	if err := CheckHealth(context.Background(), base); err == nil {
		t.Fatal("plain HTTP passed against a TLS listener")
	}
}

func TestCheckHealthRejectsBadPort(t *testing.T) {
	if err := CheckHealth(context.Background(), HealthCheckOptions{}); err == nil {
		t.Fatal("port 0 passed")
	}
}

func TestCheckHealthTakesBracketedIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	for _, host := range []string{"::1", "[::1]"} {
		if err := CheckHealth(context.Background(), HealthCheckOptions{Host: host, Port: port(srv)}); err != nil {
			t.Errorf("host %q: %v", host, err)
		}
	}
}

func TestCheckHealthLeavesNoConnectionOpen(t *testing.T) {
	var open atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	srv.Start()
	defer srv.Close()
	for range 5 {
		if err := CheckHealth(context.Background(), HealthCheckOptions{Host: "127.0.0.1", Port: port(srv)}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := open.Load(); n != 0 {
		t.Fatalf("%d connections left open", n)
	}
}

func TestHealthCheckClientUsesNoProxyAndKeepsCallerConfig(t *testing.T) {
	cfg := &tls.Config{ServerName: "role.example.invalid", MinVersion: tls.VersionTLS12}
	tr := healthCheckHTTPClient(time.Second, cfg).Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Error("the probe uses a proxy")
	}
	tr.TLSClientConfig.ServerName = "changed.example.invalid"
	if cfg.ServerName != "role.example.invalid" {
		t.Error("the caller's TLS config was changed")
	}
}
