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
		Subject: pkix.Name{CommonName: "ignored"},
		URIs:    []*url.URL{uri},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs([]string{"spiffe://example.org/svc-a"}),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	assert.Equal(t, "uri:spiffe://example.org/svc-a", identity)
}

func TestVerifyClientCertGRPC_SANDNSMatch(t *testing.T) {
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "ignored"},
		DNSNames: []string{"svc-a.internal"},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs([]string{"svc-a.internal"}),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	assert.Equal(t, "dns:svc-a.internal", identity)
}

func TestVerifyClientCertGRPC_CNMatchLegacyOnly(t *testing.T) {
	cert := &x509.Certificate{
		Subject: pkix.Name{CommonName: "svc-legacy"},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs([]string{"svc-legacy"}),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok, "CN-only allowlist must continue to authorize for legacy CAs")
	assert.Equal(t, "cn:svc-legacy", identity)
}

func TestVerifyClientCertGRPC_NoMatch(t *testing.T) {
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "svc-x"},
		DNSNames: []string{"svc-x.internal"},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs([]string{"svc-y"}),
		WithAllowedSANs([]string{"svc-y.internal"}),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.False(t, ok)
	assert.Empty(t, identity)
}

func TestVerifyClientCertGRPC_SANPreferredOverCN(t *testing.T) {
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "svc-cn"},
		DNSNames: []string{"svc-san.internal"},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedCNs([]string{"svc-cn"}),
		WithAllowedSANs([]string{"svc-san.internal"}),
	})
	ok, identity := verifyClientCertGRPC(peerCtxWith(cert), cfg)
	assert.True(t, ok)
	// SAN takes precedence over CN — the audit log carries the modern identity.
	assert.Equal(t, "dns:svc-san.internal", identity)
}

func TestVerifyClientCertGRPC_RejectsUnverifiedChain(t *testing.T) {
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "svc-cn"},
		DNSNames: []string{"svc-san.internal"},
	}
	cfg := buildMTLSIdentityConfig([]MTLSIdentityOption{
		WithAllowedSANs([]string{"svc-san.internal"}),
	})
	tlsState := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tlsState}}
	ctx := peer.NewContext(context.Background(), p)
	ok, identity := verifyClientCertGRPC(ctx, cfg)
	assert.False(t, ok, "unverified chain must be rejected even with matching SAN")
	assert.Empty(t, identity)
}
