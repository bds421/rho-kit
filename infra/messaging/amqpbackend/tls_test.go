package amqpbackend

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

func TestWithReloadingTLS_RequiresThePeerVerifier(t *testing.T) {
	reloading := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   tlsclone.PeerVerifier(func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }),
	}
	var c Connection
	WithReloadingTLS(reloading)(&c)
	assert.True(t, tlsclone.VerifiesPeer(c.tlsConfig))

	noop := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   func(tls.ConnectionState) error { return nil },
	}
	assert.Panics(t, func() { WithReloadingTLS(noop) }, "a no-op VerifyConnection must not lift the InsecureSkipVerify refusal")
	assert.Panics(t, func() { WithReloadingTLS(&tls.Config{InsecureSkipVerify: true}) })
}
