package pgx

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_RejectsEmptyDSN(t *testing.T) {
	_, err := Connect(context.Background(), Config{})
	require.Error(t, err)
}

func parseForTest(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	pcfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	return pcfg
}

// requireTLSOnParsedConfig delegates to pgxpool's parser, so the unit
// tests below assert behaviour through the same path Connect uses.

func TestRequireTLS_AcceptsRequireFamily(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			pcfg := parseForTest(t, "postgres://u:p@h/db?sslmode="+mode)
			err := requireTLSOnParsedConfig(pcfg)
			assert.NoError(t, err, "sslmode=%s must be accepted", mode)
		})
	}
}

func TestRequireTLS_RejectsLooseModes(t *testing.T) {
	for _, mode := range []string{"prefer", "allow", "disable"} {
		t.Run(mode, func(t *testing.T) {
			pcfg := parseForTest(t, "postgres://u:p@h/db?sslmode="+mode)
			err := requireTLSOnParsedConfig(pcfg)
			assert.Error(t, err, "sslmode=%s must be rejected (admits plaintext)", mode)
		})
	}
}

func TestRequireTLS_RejectsMissing(t *testing.T) {
	// pgxpool defaults sslmode to "prefer" when unset — which is itself
	// a plaintext-admitting mode and must be rejected.
	pcfg := parseForTest(t, "postgres://u:p@h/db")
	err := requireTLSOnParsedConfig(pcfg)
	assert.Error(t, err)
}

// TestRequireTLS_RejectsLastWinsBypass is the regression test for the
// N-3 audit finding: the previous extractSSLMode returned the FIRST
// sslmode= token while pgxpool honours the LAST. A DSN with both
// sslmode=require AND sslmode=disable used to slip past the kit's
// hand-rolled extractor while pgxpool actually connected plaintext.
// The pgxpool-based check sees the same posture pgxpool will use.
func TestRequireTLS_RejectsLastWinsBypass(t *testing.T) {
	pcfg := parseForTest(t, "host=h user=u dbname=db sslmode=require sslmode=disable")
	err := requireTLSOnParsedConfig(pcfg)
	assert.Error(t, err, "DSN with sslmode=disable as the last token must be rejected, regardless of earlier sslmode=require")
}

func TestRequireLoopbackHost_AcceptsLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.255.255.254", "::1"} {
		t.Run(host, func(t *testing.T) {
			err := requireLoopbackHost(host)
			assert.NoError(t, err, "host %q must be accepted", host)
		})
	}
}

func TestRequireLoopbackHost_RejectsNonLoopback(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "8.8.8.8", "evil.com", "0.0.0.0", "192.168.1.1"} {
		t.Run(host, func(t *testing.T) {
			err := requireLoopbackHost(host)
			assert.Error(t, err, "host %q must be rejected", host)
		})
	}
}

// TestRequireLoopbackHost_RejectsHostBypass covers the N-2 finding:
// the previous extractDSNHost ignored URL query-string `?host=` and
// took the first `host=` in libpq form. Going through pgxpool.ParseConfig
// + ConnConfig.Host avoids both gaps because pgxpool's parser is
// authoritative on which host the runtime will dial.
func TestRequireLoopbackHost_RejectsParsedNonLoopback(t *testing.T) {
	// URL form with ?host= override — pgxpool honors this as the host.
	pcfg := parseForTest(t, "postgres://u:p@127.0.0.1/db?host=10.0.0.5")
	err := requireLoopbackHost(pcfg.ConnConfig.Host)
	assert.Error(t, err, "URL-form ?host=remote must be rejected at loopback gate")

	// Libpq form with two host= tokens — pgxpool takes the last.
	pcfg = parseForTest(t, "host=127.0.0.1 host=10.0.0.5 user=u dbname=db")
	err = requireLoopbackHost(pcfg.ConnConfig.Host)
	assert.Error(t, err, "libpq-form last-wins host=remote must be rejected at loopback gate")
}
