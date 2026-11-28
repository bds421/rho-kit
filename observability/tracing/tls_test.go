package tracing

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
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

const collectorName = "collector.example.invalid"

// testCA issues the collector's and the workloads' certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
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
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
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
	return &testCA{cert: cert, key: key, pool: pool}
}

func (ca *testCA) issue(t *testing.T, name string, usage x509.ExtKeyUsage) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
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
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// mtlsCollector is a gRPC server that requires a client certificate from
// the test CA and records the common name of each export's client.
type mtlsCollector struct {
	addr  string
	creds credentials.TransportCredentials
	mu    sync.Mutex
	names []string
	srv   *grpc.Server
}

func newMTLSCollector(t *testing.T, ca *testCA) *mtlsCollector {
	t.Helper()
	c := &mtlsCollector{
		addr: "127.0.0.1:0",
		creds: credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{*ca.issue(t, collectorName, x509.ExtKeyUsageServerAuth)},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    ca.pool,
		}),
	}
	c.serve(t)
	return c
}

// serve starts a server on the collector's address; the previous one, if
// any, ends its connections first, so the next export dials a new one.
func (c *mtlsCollector) serve(t *testing.T) {
	t.Helper()
	if c.srv != nil {
		c.srv.Stop()
	}
	lis, err := net.Listen("tcp", c.addr)
	if err != nil {
		t.Fatal(err)
	}
	c.addr = lis.Addr().String()
	srv := grpc.NewServer(grpc.Creds(c.creds), grpc.UnknownServiceHandler(c.export))
	c.srv = srv
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}

func (c *mtlsCollector) export(_ any, stream grpc.ServerStream) error {
	if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
		return err
	}
	p, ok := peer.FromContext(stream.Context())
	if !ok {
		return errors.New("no peer")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return errors.New("no client certificate")
	}
	c.mu.Lock()
	c.names = append(c.names, info.State.PeerCertificates[0].Subject.CommonName)
	c.mu.Unlock()
	// An empty ExportTraceServiceResponse has the wire form of Empty.
	return stream.SendMsg(&emptypb.Empty{})
}

func (c *mtlsCollector) clients() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.names...)
}

func exportOne(t *testing.T, p *Provider) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, span := p.Tracer("test").Start(ctx, "op")
	span.End()
	return p.tp.ForceFlush(ctx)
}

// The exporter presents the certificate GetClientCertificate returns at each
// handshake to a collector that requires one, so a renewed certificate is
// used on the next connection.
func TestInit_ExportsOverMutualTLSWithReloadingClientCertificate(t *testing.T) {
	ca := newTestCA(t)
	collector := newMTLSCollector(t, ca)

	var current atomic.Pointer[tls.Certificate]
	current.Store(ca.issue(t, "workload-a", x509.ExtKeyUsageClientAuth))
	p, err := Init(context.Background(), Config{
		ServiceName: "test",
		Endpoint:    collector.addr,
		SampleRate:  1,
		TLS: &tls.Config{
			// The form of security/netutil.ReloadingClientTLS: the CA pool is
			// read per handshake too.
			InsecureSkipVerify: true, //nolint:gosec // VerifyConnection verifies
			VerifyConnection:   tlsclone.PeerVerifier(func() (*x509.CertPool, error) { return ca.pool, nil }),
			ServerName:         collectorName,
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				return current.Load(), nil
			},
		},
		OnInitFallback: func(err error) { t.Errorf("fallback: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	if err := exportOne(t, p); err != nil {
		t.Fatalf("export: %v", err)
	}
	current.Store(ca.issue(t, "workload-b", x509.ExtKeyUsageClientAuth))
	collector.serve(t)
	if err := exportOne(t, p); err != nil {
		t.Fatalf("export after renewal: %v", err)
	}

	if got := strings.Join(collector.clients(), ","); got != "workload-a,workload-b" {
		t.Fatalf("clients = %q, want workload-a,workload-b", got)
	}
}

// Without a client certificate the collector refuses the handshake and no
// export arrives.
func TestInit_MutualTLSCollectorRefusesExporterWithoutCertificate(t *testing.T) {
	ca := newTestCA(t)
	collector := newMTLSCollector(t, ca)

	p, err := Init(context.Background(), Config{
		ServiceName: "test",
		Endpoint:    collector.addr,
		SampleRate:  1,
		TLS:         &tls.Config{RootCAs: ca.pool, ServerName: collectorName},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, span := p.Tracer("test").Start(ctx, "op")
	span.End()
	if err := p.tp.ForceFlush(ctx); err == nil {
		t.Fatal("export without a client certificate succeeded")
	}
	if got := collector.clients(); len(got) != 0 {
		t.Fatalf("clients = %v, want none", got)
	}
}

func TestConfigValidate_TLS(t *testing.T) {
	base := Config{ServiceName: "svc", Endpoint: "collector:4317"}

	both := base
	both.Insecure = true
	both.TLS = &tls.Config{}
	if err := both.Validate(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("Insecure with TLS: err = %v", err)
	}

	skip := base
	skip.TLS = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the case under test
	if err := skip.Validate(); err == nil {
		t.Fatal("InsecureSkipVerify without a peer verifier passed")
	}

	low := base
	low.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
	if err := low.Validate(); err == nil {
		t.Fatal("MaxVersion below TLS 1.3 passed")
	}

	ok := base
	ok.TLS = &tls.Config{}
	if err := ok.Validate(); err != nil {
		t.Fatalf("plain TLS: %v", err)
	}
}
