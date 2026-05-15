package interceptor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// peerCtxWith returns a context carrying a peer with the supplied verified
// certificate, mirroring what gRPC's TLS layer attaches after a successful
// mTLS handshake.
func peerCtxWith(cert *x509.Certificate) context.Context {
	tlsState := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tlsState}}
	return peer.NewContext(context.Background(), p)
}

func TestVerifyClientCertGRPC_SANURIMatch(t *testing.T) {
	uri, err := url.Parse("spiffe://example.org/svc-a")
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ignored"},
		URIs:        []*url.URL{uri},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs("spiffe://example.org/svc-a"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	assert.Equal(t, "uri:spiffe://example.org/svc-a", identity)
}

func TestVerifyClientCertGRPC_SANDNSMatch(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ignored"},
		DNSNames:    []string{"svc-a.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs("svc-a.internal"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	assert.Equal(t, "dns:svc-a.internal", identity)
}

func TestVerifyClientCertGRPC_SANDNSMatchIsCaseInsensitive(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ignored"},
		DNSNames:    []string{"svc-a.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs("SVC-A.INTERNAL"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	assert.Equal(t, "dns:svc-a.internal", identity)
}

func TestVerifyClientCertGRPC_CNMatchLegacyOnly(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-legacy"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs("svc-legacy"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok, "CN-only allowlist must continue to authorize for legacy CAs")
	assert.Equal(t, "cn:svc-legacy", identity)
}

func TestVerifyClientCertGRPC_NoMatch(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-x"},
		DNSNames:    []string{"svc-x.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs("svc-y"),
		WithAllowedSANs("svc-y.internal"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.False(t, ok)
	assert.Empty(t, identity)
}

func TestVerifyClientCertGRPC_SANPreferredOverCN(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-cn"},
		DNSNames:    []string{"svc-san.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs("svc-cn"),
		WithAllowedSANs("svc-san.internal"),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	// SAN takes precedence over CN — the audit log carries the modern identity.
	assert.Equal(t, "dns:svc-san.internal", identity)
}

func TestVerifyClientCertGRPC_RejectsUnverifiedChain(t *testing.T) {
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-cn"},
		DNSNames:    []string{"svc-san.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs("svc-san.internal"),
	})
	tlsState := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tlsState}}
	ctx := peer.NewContext(context.Background(), p)
	ok, identity := verifyClientCertGRPC(ctx, cfg)
	assert.False(t, ok, "unverified chain must be rejected even with matching SAN")
	assert.Empty(t, identity)
}

func TestVerifyClientCertGRPC_RejectsCertWithoutClientAuthEKU(t *testing.T) {
	for _, tt := range []struct {
		name string
		eku  []x509.ExtKeyUsage
	}{
		{name: "no EKU", eku: nil},
		{name: "server auth only", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cert := &x509.Certificate{
				Subject:     pkix.Name{CommonName: "svc-cn"},
				DNSNames:    []string{"svc-san.internal"},
				ExtKeyUsage: tt.eku,
			}
			cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
				WithAllowedSANs("svc-san.internal"),
			})
			ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
			assert.False(t, ok)
			assert.Empty(t, identity)
		})
	}
}

func TestWithAllowedSANsRejectsInvalidEntries(t *testing.T) {
	for _, san := range []string{
		"svc name.internal",
		"svc/internal",
		"svc_name.internal",
		"-svc.internal",
		"svc-.internal",
		"*.internal",
		string([]byte{'s', 'v', 'c', 0xff}),
		"spiffe://example.org/svc-a?debug=true",
		"spiffe://user@example.org/svc-a",
		"spiffe://example.org/svc-a#frag",
	} {
		t.Run(san, func(t *testing.T) {
			assert.Panics(t, func() {
				buildMTLSIdentityConfig([]MTLSIdentityOption{WithAllowedSANs(san)})
			})
		})
	}
}

func TestWithAllowedCNsRejectsInvalidEntries(t *testing.T) {
	for _, cn := range []string{
		"svc\nname",
		"svc\tname",
		"svc\x00name",
		string([]byte{'s', 'v', 'c', 0xff}),
	} {
		t.Run(cn, func(t *testing.T) {
			assert.Panics(t, func() {
				buildMTLSIdentityConfig([]MTLSIdentityOption{WithAllowedCNs(cn)})
			})
		})
	}
}

func TestWithAllowedIdentityPanicsDoNotEchoValues(t *testing.T) {
	assert.PanicsWithValue(t, "grpcx/interceptor: WithAllowedSANs invalid URI SAN", func() {
		buildMTLSIdentityConfig([]MTLSIdentityOption{
			WithAllowedSANs("spiffe://example.org/%zz?token=secret-token"),
		})
	})
	assert.PanicsWithValue(t, "grpcx/interceptor: WithAllowedCNs invalid CN", func() {
		buildMTLSIdentityConfig([]MTLSIdentityOption{
			WithAllowedCNs("svc\nsecret-token"),
		})
	})
}
