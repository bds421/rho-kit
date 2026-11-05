package tlsclone

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

func noCAs() (*x509.CertPool, error) { return x509.NewCertPool(), nil }

func TestVerifiesPeer_RecognisesOnlyPeerVerifier(t *testing.T) {
	verified := &tls.Config{InsecureSkipVerify: true, VerifyConnection: PeerVerifier(noCAs)}
	other := &tls.Config{InsecureSkipVerify: true, VerifyConnection: PeerVerifier(func() (*x509.CertPool, error) {
		return nil, errors.New("unused")
	})}
	noop := &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(tls.ConnectionState) error { return nil }}
	wrapped := &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(s tls.ConnectionState) error {
		return verified.VerifyConnection(s)
	}}

	cases := []struct {
		name string
		cfg  *tls.Config
		want bool
	}{
		{"PeerVerifier", verified, true},
		{"PeerVerifier with another source", other, true},
		{"clone of a PeerVerifier config", verified.Clone(), true},
		{"no-op VerifyConnection", noop, false},
		{"VerifyConnection wrapping a PeerVerifier", wrapped, false},
		{"no VerifyConnection", &tls.Config{InsecureSkipVerify: true}, false},
		{"nil config", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifiesPeer(tc.cfg); got != tc.want {
				t.Fatalf("VerifiesPeer = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigWithFloor_AllowPeerVerifierRefusesOtherSkipVerify(t *testing.T) {
	verified := &tls.Config{InsecureSkipVerify: true, VerifyConnection: PeerVerifier(noCAs)}
	cloned, err := ConfigWithFloor(verified, tls.VersionTLS12, WithAllowPeerVerifier())
	if err != nil {
		t.Fatalf("PeerVerifier config refused: %v", err)
	}
	if !VerifiesPeer(cloned) {
		t.Fatal("the clone must still verify through the PeerVerifier")
	}

	noop := &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(tls.ConnectionState) error { return nil }}
	if _, err := ConfigWithFloor(noop, tls.VersionTLS12, WithAllowPeerVerifier()); !errors.Is(err, ErrInsecureSkipVerifyNotPermitted) {
		t.Fatalf("no-op VerifyConnection: err = %v, want ErrInsecureSkipVerifyNotPermitted", err)
	}
	if _, err := ConfigWithFloor(verified, tls.VersionTLS12); !errors.Is(err, ErrInsecureSkipVerifyNotPermitted) {
		t.Fatalf("without the option: err = %v, want ErrInsecureSkipVerifyNotPermitted", err)
	}
}

func TestPeerVerifier_VerifiesChainAndServerName(t *testing.T) {
	ca, caKey := newTestCert(t, nil, nil, "test ca", nil)
	leaf, _ := newTestCert(t, ca, caKey, "leaf", []string{"service.test"})
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	verify := PeerVerifier(func() (*x509.CertPool, error) { return pool, nil })

	if err := verify(tls.ConnectionState{ServerName: "service.test", PeerCertificates: []*x509.Certificate{leaf}}); err != nil {
		t.Fatalf("valid peer refused: %v", err)
	}
	if err := verify(tls.ConnectionState{ServerName: "other.test", PeerCertificates: []*x509.Certificate{leaf}}); err == nil {
		t.Fatal("a peer for another name must be refused")
	}
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}); !errors.Is(err, ErrServerNameRequired) {
		t.Fatalf("empty server name: err = %v, want ErrServerNameRequired", err)
	}
	if err := verify(tls.ConnectionState{ServerName: "service.test"}); err == nil {
		t.Fatal("a peer without certificates must be refused")
	}
	untrusted := PeerVerifier(noCAs)
	if err := untrusted(tls.ConnectionState{ServerName: "service.test", PeerCertificates: []*x509.Certificate{leaf}}); err == nil {
		t.Fatal("a peer outside the CA pool must be refused")
	}
}

func TestPeerVerifier_PanicsOnNilSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("PeerVerifier(nil) must panic")
		}
	}()
	PeerVerifier(nil)
}

func newTestCert(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, cn string, dns []string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dns,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	signer, signerKey := tmpl, key
	if parent == nil {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
	} else {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}
