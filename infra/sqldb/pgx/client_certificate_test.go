package pgx

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyClientCertificate_EachHandshakeAsksSource(t *testing.T) {
	pcfg := parseForTest(t, "host=db1.example.com,db2.example.com user=u dbname=db sslmode=verify-full")
	require.Len(t, pcfg.ConnConfig.Fallbacks, 1)
	current := &tls.Certificate{Certificate: [][]byte{{1}}}
	require.NoError(t, applyClientCertificate(pcfg, func() (*tls.Certificate, error) { return current, nil }))

	// pgxpool dials with a copy of ConnConfig per connection.
	conn := pcfg.ConnConfig.Copy()
	for _, tc := range []*tls.Config{conn.TLSConfig, conn.Fallbacks[0].TLSConfig} {
		require.NotNil(t, tc.GetClientCertificate)
		got, err := tc.GetClientCertificate(&tls.CertificateRequestInfo{})
		require.NoError(t, err)
		assert.Same(t, current, got)
	}

	renewed := &tls.Certificate{Certificate: [][]byte{{2}}}
	current = renewed
	got, err := conn.TLSConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)
	assert.Same(t, renewed, got)
	assert.False(t, conn.TLSConfig.InsecureSkipVerify, "verify-full keeps stdlib verification")
}

func TestApplyClientCertificate_SourceFailures(t *testing.T) {
	pcfg := parseForTest(t, "host=db.example.com user=u dbname=db sslmode=verify-full")
	require.NoError(t, applyClientCertificate(pcfg, func() (*tls.Certificate, error) { return nil, nil }))
	_, err := pcfg.ConnConfig.TLSConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.ErrorContains(t, err, "returned no certificate")

	pcfg = parseForTest(t, "host=db.example.com user=u dbname=db sslmode=verify-full")
	cause := errors.New("renewal pending")
	require.NoError(t, applyClientCertificate(pcfg, func() (*tls.Certificate, error) { return nil, cause }))
	_, err = pcfg.ConnConfig.TLSConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
}

func TestApplyClientCertificate_RejectsDSNCertificate(t *testing.T) {
	pcfg := parseForTest(t, "host=db.example.com user=u dbname=db sslmode=verify-full")
	pcfg.ConnConfig.TLSConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{{1}}}}
	err := applyClientCertificate(pcfg, func() (*tls.Certificate, error) { return &tls.Certificate{}, nil })
	require.ErrorContains(t, err, "not both")
}

func TestConnect_ClientCertificateRequiresTLS(t *testing.T) {
	_, err := Connect(context.Background(), Config{
		DSN:                            "postgres://u@127.0.0.1/db?sslmode=disable",
		AllowPlaintextLoopbackForTests: true,
		ClientCertificate:              func() (*tls.Certificate, error) { return &tls.Certificate{}, nil },
	})
	require.ErrorContains(t, err, "requires a DSN that enables TLS")
}

func TestApplyClientCertificate_NilSourceLeavesConfig(t *testing.T) {
	pcfg := parseForTest(t, "host=db.example.com user=u dbname=db sslmode=verify-full")
	require.NoError(t, applyClientCertificate(pcfg, nil))
	assert.Nil(t, pcfg.ConnConfig.TLSConfig.GetClientCertificate)
}
