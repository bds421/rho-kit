package redis

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

type staticCertSource struct{}

func (staticCertSource) ServerCertificate() (*tls.Certificate, error) { return &tls.Certificate{}, nil }
func (staticCertSource) ClientCertificate() (*tls.Certificate, error) { return &tls.Certificate{}, nil }
func (staticCertSource) CAs() (*x509.CertPool, error)                 { return x509.NewCertPool(), nil }

func TestModule_AcceptsReloadingClientTLS(t *testing.T) {
	m := Module(&goredis.Options{
		Addr:      "redis.example.com:6380",
		Password:  "secret",
		TLSConfig: netutil.ReloadingClientTLS(staticCertSource{}),
	}).(*redisModule)

	require.NotNil(t, m.opts.TLSConfig)
	assert.True(t, netutil.IsReloadingClientTLS(m.opts.TLSConfig), "the clone keeps the kit's peer verifier")
	assert.NotNil(t, m.opts.TLSConfig.GetClientCertificate)
}

func TestModule_PanicsOnInsecureSkipVerifyWithoutPeerVerifier(t *testing.T) {
	for name, cfg := range map[string]*tls.Config{
		"no VerifyConnection": {InsecureSkipVerify: true},
		"own VerifyConnection": {
			InsecureSkipVerify: true,
			VerifyConnection:   func(tls.ConnectionState) error { return nil },
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.PanicsWithValue(t, "redis: TLS InsecureSkipVerify=true is not permitted", func() {
				_ = Module(&goredis.Options{Addr: "redis.example.com:6380", Password: "secret", TLSConfig: cfg})
			})
		})
	}
}

func TestModule_PeerVerifierCloneStaysVerifying(t *testing.T) {
	cfg := &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection:   tlsclone.PeerVerifier(func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }),
	}
	m := Module(&goredis.Options{Addr: "localhost:6380", TLSConfig: cfg}).(*redisModule)
	assert.True(t, tlsclone.VerifiesPeer(m.opts.TLSConfig))
}
