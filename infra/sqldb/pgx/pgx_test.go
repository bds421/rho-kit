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
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.255.255.254", "::1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			err := requireLoopbackHost(host)
			assert.NoError(t, err, "host %q must be accepted", host)
		})
	}
}

// TestConnect_RejectsMultiHostNonLoopbackFallback pins the N-6 audit
// fix: pgx supports comma-separated multi-host DSNs. The primary host
// lands on ConnConfig.Host; additional hosts become ConnConfig.Fallbacks
// entries. The previous version of the loopback gate only checked the
// primary, letting a DSN like
//
//	host=localhost,evil.example.com sslmode=disable
//
// pass while pgx silently failed over to evil.example.com sending
// plaintext credentials. The fix walks every Host across the parsed
// config + every fallback.
func TestConnect_RejectsMultiHostNonLoopbackFallback(t *testing.T) {
	dsn := "host=localhost,evil.example.com user=u password=p dbname=db sslmode=disable"
	_, err := Connect(context.Background(), Config{
		DSN:                            dsn,
		AllowPlaintextLoopbackForTests: true,
	})
	require.Error(t, err, "multi-host DSN with non-loopback fallback must be rejected")
	assert.Contains(t, err.Error(), "loopback")
}

// TestConnect_RejectsMultiHostURLFormFallback covers the URL form of
// the N-6 multi-host bypass.
func TestConnect_RejectsMultiHostURLFormFallback(t *testing.T) {
	dsn := "postgres://u:p@127.0.0.1:5432,evil.example.com:5432/db?sslmode=disable"
	_, err := Connect(context.Background(), Config{
		DSN:                            dsn,
		AllowPlaintextLoopbackForTests: true,
	})
	require.Error(t, err, "URL-form multi-host DSN with non-loopback fallback must be rejected")
	assert.Contains(t, err.Error(), "loopback")
}

func TestRequireLoopbackHost_RejectsNonLoopback(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "8.8.8.8", "evil.com", "0.0.0.0", "192.168.1.1"} {
		t.Run(host, func(t *testing.T) {
			err := requireLoopbackHost(host)
			assert.Error(t, err, "host %q must be rejected", host)
		})
	}
}

// TestParseCopyIdentifier covers the schema-qualified table fix.
// pgx.Identifier{table} quoted the whole "schema.table" string as one
// identifier, which Postgres rejects. The fix splits on the first dot
// and lets pgx emit "schema"."table".
func TestParseCopyIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "bare", in: "users", want: []string{"users"}},
		{name: "schema-qualified", in: "public.users", want: []string{"public", "users"}},
		{name: "three-segments", in: "db.public.users", wantErr: true},
		{name: "trailing dot", in: "public.", wantErr: true},
		{name: "leading dot", in: ".users", wantErr: true},
		{name: "empty middle", in: ".", wantErr: true},
		{name: "embedded quote", in: "public.\"users", wantErr: true},
		{name: "embedded null byte", in: "public.us\x00ers", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCopyIdentifier(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, []string(got))
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
