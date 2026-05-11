package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/tracing"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// newSafeBuilder returns a Builder with the always-on production-safety
// validator armed and the TLS / audience opt-outs applied. The TLS,
// internal-host, and audience checks have dedicated tests below; the
// helper isolates each remaining test to a single concern.
func newSafeBuilder() *Builder {
	return New("test", "v1", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience()
}

func TestBuilder_Validates_NoOpWithoutJWT(t *testing.T) {
	// A service that doesn't enable JWT must still pass validation.
	require.NoError(t, newSafeBuilder().Validate())
}

func TestBuilder_Validates_RejectsJWTWithoutIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json")
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestBuilder_Validates_AcceptsJWTWithIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	require.NoError(t, b.Validate())
}

func TestBuilder_Validates_AcceptsJWTWithoutJWTIssuer(t *testing.T) {
	b := newSafeBuilder().
		WithJWT("https://example.com/.well-known/jwks.json").
		WithoutJWTIssuer()
	require.NoError(t, b.Validate())
}

// Postgres TLS validation is now enforced inside the pgx package's
// Connect — by the time Builder.Run reaches WithPostgres, the DSN is
// passed through. The validator no longer pre-parses the DSN, so the
// equivalent test belongs in infra/sqldb/pgx, not here.

func TestBuilder_Validates_TracingSampleRateCapped(t *testing.T) {
	b := newSafeBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 1.0})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SampleRate")
}

func TestBuilder_Validates_TracingAcceptsLowSampleRate(t *testing.T) {
	b := newSafeBuilder().WithTracing(tracing.Config{ServiceName: "test", SampleRate: 0.05})
	require.NoError(t, b.Validate())
}

func TestBuilder_Validates_TracingConfig(t *testing.T) {
	b := newSafeBuilder().WithTracing(tracing.Config{
		ServiceName: "test",
		Endpoint:    "https://collector.example.com:4317",
	})
	err := b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Endpoint")
}

// --- C-1: internal ops port must default to loopback ---

func TestInternalConfig_DefaultsToLoopback(t *testing.T) {
	// LoadBaseConfig uses environment variables; clear INTERNAL_HOST so
	// the default applies regardless of the host environment.
	t.Setenv("INTERNAL_HOST", "")
	cfg, err := LoadBaseConfig(8080)
	require.NoError(t, err)
	assert.NotEqual(t, "0.0.0.0", cfg.Internal.Addr(),
		"internal ops port must not bind to 0.0.0.0 by default — exposes /metrics publicly")
	assert.Contains(t, cfg.Internal.Addr(), "127.0.0.1",
		"internal ops port should default to loopback")
}

func TestBuilder_Validates_RejectsExposedInternal(t *testing.T) {
	cfg := BaseConfig{
		Server:   ServerConfig{Port: 8080},
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(t),
	}
	b := New("svc", "v1", cfg).
		WithoutJWTAudience()
	err := b.Validate()
	require.Error(t, err, "exposed internal port must fail validation")
	assert.Contains(t, err.Error(), "Internal.Host")
	assert.Contains(t, err.Error(), "WithInternalNonLoopback")
}

func TestWithInternalNonLoopback_AcceptsOptIn(t *testing.T) {
	cfg := BaseConfig{
		Server:   ServerConfig{Port: 8080},
		Internal: InternalConfig{Host: "0.0.0.0", Port: 9090},
		TLS:      validTLSForTest(t),
	}
	b := New("svc", "v1", cfg).
		WithInternalNonLoopback().
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithInternalNonLoopback must allow Internal.Host=0.0.0.0")
}

// TestBuilder_Validates_RejectsSpecificNonLoopbackInternal regression-tests
// FR-010 [HIGH]: pre-fix, the validator only rejected wildcard hosts
// (0.0.0.0, [::]). A specific non-loopback IP like 10.0.0.5 — or any
// hostname that resolves to a routable interface — slipped through
// silently and exposed unauthenticated /metrics on the network.
// The fix flipped the contract from "reject unspecified" to "require
// loopback".
func TestBuilder_Validates_RejectsSpecificNonLoopbackInternal(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "192.168.1.1", "172.16.0.1", "8.8.8.8", "secret-token.example"} {
		t.Run(host, func(t *testing.T) {
			cfg := BaseConfig{
				Server:   ServerConfig{Port: 8080},
				Internal: InternalConfig{Host: host, Port: 9090},
				TLS:      validTLSForTest(t),
			}
			b := New("svc", "v1", cfg).WithoutJWTAudience()
			err := b.Validate()
			require.Errorf(t, err, "Internal.Host=%q must be rejected without WithInternalNonLoopback", host)
			assert.Contains(t, err.Error(), "not loopback")
			assert.Contains(t, err.Error(), "WithInternalNonLoopback")
			assert.NotContains(t, err.Error(), host)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestBuilder_Validates_AcceptsLoopbackVariants(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "127.0.0.2", "::1", "[::1]", "localhost"} {
		t.Run(host, func(t *testing.T) {
			cfg := BaseConfig{
				Server:   ServerConfig{Port: 8080},
				Internal: InternalConfig{Host: host, Port: 9090},
				TLS:      validTLSForTest(t),
			}
			b := New("svc", "v1", cfg).WithoutJWTAudience()
			err := b.Validate()
			require.NoErrorf(t, err, "Internal.Host=%q is loopback and must pass", host)
		})
	}
}

// TestBuilder_Validates_RejectsIPv6Wildcard pins the M-A audit fix:
// the C-1 check used to compare the literal string "0.0.0.0", missing
// the IPv6 wildcard [::] (and other unspecified-address forms).
// Operators setting INTERNAL_HOST=[::] would have bypassed the check
// and bound /metrics to all IPv6 interfaces.
func TestBuilder_Validates_RejectsIPv6Wildcard(t *testing.T) {
	for _, host := range []string{"::", "[::]", "0:0:0:0:0:0:0:0"} {
		t.Run(host, func(t *testing.T) {
			cfg := BaseConfig{
				Server:   ServerConfig{Port: 8080},
				Internal: InternalConfig{Host: host, Port: 9090},
				TLS:      validTLSForTest(t),
			}
			b := New("svc", "v1", cfg).WithoutJWTAudience()
			err := b.Validate()
			require.Error(t, err, "IPv6 wildcard %q must fail validation", host)
			assert.Contains(t, err.Error(), "exposes unauthenticated /metrics")
		})
	}
}

// TestBuilder_Validates_RejectsIPv4ZeroForms pins the N-1 audit fix:
// the previous net.ParseIP-only check rejected the canonical "0.0.0.0"
// but accepted leading-zero or short-form variants like "00.00.00.00",
// "0", "0.0" — all of which net.Listen interprets as the IPv4 wildcard
// even though net.ParseIP rejects them as malformed.
//
// The hex-encoded variants (0x0, 0X00000000, mixed forms) are the N-7
// audit fix — net.Listen accepts these through octal/hex IPv4 parsing.
func TestBuilder_Validates_RejectsIPv4ZeroForms(t *testing.T) {
	hosts := []string{
		// N-1: leading-zero / short decimal forms
		"00.00.00.00",
		"000.000.000.000",
		"0",
		"0.0",
		"0.0.0",
		"0.00.00.00",
		// N-7: hex-encoded zeros
		"0x0",
		"0X0",
		"0x00000000",
		"0X00000000",
		"0x0.0x0.0x0.0x0",
		"0X0.0X0.0X0.0X0",
		"0x00.0x00.0x00.0x00",
		"0x0.0",
		"0.0X0",
		// N-9: single-segment numeric overflow that cgo's getaddrinfo
		// truncates to zero (4294967296 = 2^32, etc.). The previous
		// strconv.ParseUint walk accepted these because the value
		// before truncation is non-zero. The ResolveTCPAddr-based check
		// catches them because Go's address parser delegates to the
		// same code net.Listen uses.
		"4294967296",
		"0x100000000",
		"0X100000000",
		"040000000000",
		"8589934592",
		// N-10: bracket-only forms strip to empty; net.Listen treats
		// "[]:port" as the IPv6 wildcard and binds [::]:port. The
		// validator must catch all three bracket-only inputs.
		"[]",
		"[",
		"]",
	}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			cfg := BaseConfig{
				Server:   ServerConfig{Port: 8080},
				Internal: InternalConfig{Host: host, Port: 9090},
				TLS:      validTLSForTest(t),
			}
			b := New("svc", "v1", cfg).WithoutJWTAudience()
			err := b.Validate()
			require.Error(t, err, "IPv4 zero form %q must fail validation", host)
			assert.Contains(t, err.Error(), "exposes unauthenticated /metrics")
		})
	}
}

// --- C-2: validator requires TLS ---

func TestBuilder_Validates_RequiresTLS(t *testing.T) {
	b := New("svc", "v1", validBaseConfig()).
		WithoutJWTAudience()
	err := b.Validate()
	require.Error(t, err, "validator must reject empty TLS config")
	assert.Contains(t, err.Error(), "TLS")
	assert.Contains(t, err.Error(), "WithoutTLS")
}

func TestWithoutTLS_AcceptsOptIn(t *testing.T) {
	b := New("svc", "v1", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithoutTLS must allow empty TLS config")
}

// --- H-4: WithTenantBudget requires WithMultiTenant ---

func TestBudget_RequiresMultiTenant(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithTenantBudget(&stubBudget{})
	err := b.Validate()
	require.Error(t, err, "WithTenantBudget without WithMultiTenant must fail")
	assert.Contains(t, err.Error(), "WithMultiTenant")
}

func TestBudget_WithMultiTenant_Passes(t *testing.T) {
	b := New("test", "v1", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience().
		WithMultiTenant(nil, true).
		WithTenantBudget(&stubBudget{})
	require.NoError(t, b.Validate(),
		"WithTenantBudget paired with WithMultiTenant must pass validation")
}

// --- H-5: validator requires WithJWTAudience ---

func TestBuilder_Validates_RequiresJWTAudience(t *testing.T) {
	cfg := validBaseConfig()
	cfg.TLS = validTLSForTest(t)
	b := New("svc", "v1", cfg).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com")
	err := b.Validate()
	require.Error(t, err, "validator must require WithJWTAudience to mitigate confused-deputy")
	assert.Contains(t, err.Error(), "WithJWTAudience")
}

func TestBuilder_Validates_AcceptsJWTAudience(t *testing.T) {
	cfg := validBaseConfig()
	cfg.TLS = validTLSForTest(t)
	b := New("svc", "v1", cfg).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithJWTAudience("svc")
	require.NoError(t, b.Validate(),
		"WithJWTAudience must satisfy the audience check")
}

func TestBuilder_Validates_AcceptsWithoutJWTAudience(t *testing.T) {
	cfg := validBaseConfig()
	cfg.TLS = validTLSForTest(t)
	b := New("svc", "v1", cfg).
		WithJWT("https://example.com/.well-known/jwks.json").
		WithJWTIssuer("https://issuer.example.com").
		WithoutJWTAudience()
	require.NoError(t, b.Validate(),
		"WithoutJWTAudience must satisfy the audience check")
}

// --- FR-014: Builder TLS defaults to mTLS ---

// TestBuilder_PublicTLSDefaultsToMutualAuth pins the FR-014 [HIGH]
// fix: the Builder's default for the public listener must be
// tls.RequireAndVerifyClientCert. Pre-fix, ServerTLS was called
// without options and produced VerifyClientCertIfGiven, contradicting
// the kit's documented "TLS env enables global mTLS" convention.
func TestBuilder_PublicTLSDefaultsToMutualAuth(t *testing.T) {
	tlsCfg := realTLSForTest(t)
	cfg := validBaseConfig()
	cfg.TLS = tlsCfg
	b := New("svc", "v1", cfg).WithoutJWTAudience()

	got, err := tlsCfg.ServerTLS(b.serverTLSOptions()...)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, tls.RequireAndVerifyClientCert, got.ClientAuth,
		"public TLS server must require client certificates by default (FR-014)")
}

// TestBuilder_WithOptionalClientCertificates_DowngradesToVerifyIfGiven
// covers the documented escape hatch for gateway-fronted services.
func TestBuilder_WithOptionalClientCertificates_DowngradesToVerifyIfGiven(t *testing.T) {
	tlsCfg := realTLSForTest(t)
	cfg := validBaseConfig()
	cfg.TLS = tlsCfg
	b := New("svc", "v1", cfg).
		WithoutJWTAudience().
		WithOptionalClientCertificates()

	got, err := tlsCfg.ServerTLS(b.serverTLSOptions()...)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, tls.VerifyClientCertIfGiven, got.ClientAuth,
		"WithOptionalClientCertificates must opt out of mTLS")
}

// realTLSForTest writes a self-signed CA + leaf certificate to temp
// files so tls.LoadX509KeyPair succeeds and the resulting *tls.Config
// reflects the ClientAuth choice we want to assert.
func realTLSForTest(t *testing.T) netutil.TLSConfig {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)

	dir := t.TempDir()
	caPath := dir + "/ca.pem"
	certPath := dir + "/cert.pem"
	keyPath := dir + "/key.pem"
	writePEM(t, caPath, "CERTIFICATE", der)
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
	return netutil.TLSConfig{CACert: caPath, Cert: certPath, Key: keyPath}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, pem.Encode(f, &pem.Block{Type: typ, Bytes: der}))
}

// validTLSForTest returns a TLSConfig backed by readable temp files so
// ValidateBase()'s file-accessibility check passes. The contents are
// empty — only the Enabled() and existence checks run during
// Builder.Validate().
func validTLSForTest(t *testing.T) netutil.TLSConfig {
	t.Helper()
	dir := t.TempDir()
	paths := make(map[string]string, 3)
	for _, name := range []string{"ca.pem", "cert.pem", "key.pem"} {
		p := dir + "/" + name
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		_ = f.Close()
		paths[name] = p
	}
	return netutil.TLSConfig{
		CACert: paths["ca.pem"],
		Cert:   paths["cert.pem"],
		Key:    paths["key.pem"],
	}
}
