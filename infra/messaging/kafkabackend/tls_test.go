package kafkabackend

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

func TestCloneTLSConfigWithFloor_SkipVerifyOnlyWithThePeerVerifier(t *testing.T) {
	reloading := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   tlsclone.PeerVerifier(func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }),
	}
	cloned, err := cloneTLSConfigWithFloor(reloading)
	require.NoError(t, err)
	assert.True(t, tlsclone.VerifiesPeer(cloned))

	noop := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   func(tls.ConnectionState) error { return nil },
	}
	_, err = cloneTLSConfigWithFloor(noop)
	require.Error(t, err, "a no-op VerifyConnection must not lift the InsecureSkipVerify refusal")
	assert.Contains(t, err.Error(), "InsecureSkipVerify=true is not permitted")
}
