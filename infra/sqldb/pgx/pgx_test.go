package pgx

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = Config{}

type listenContextKey struct{}

func TestConfig_LogValue_RedactsURLDSN(t *testing.T) {
	cfg := Config{
		DSN:                            "postgres://token-user:secret-pass@db.example.com/app?sslmode=verify-full&password=query-secret",
		AllowSSLModeRequire:            true,
		MaxConns:                       10,
		HealthCheckPeriod:              5,
		MaxConnLifetime:                7,
		AllowPlaintextLoopbackForTests: true,
	}

	rendered := cfg.LogValue().String()

	assert.Contains(t, rendered, "dsn_configured=true")
	assert.NotContains(t, rendered, "token-user")
	assert.NotContains(t, rendered, "secret-pass")
	assert.NotContains(t, rendered, "query-secret")
	assert.NotContains(t, rendered, "db.example.com")
	assert.NotContains(t, rendered, "/app")
}

func TestConfig_LogValue_RedactsKeywordValueDSN(t *testing.T) {
	cfg := Config{DSN: "host=db.example.com user=app password=secret-pass dbname=app sslmode=verify-full"}

	rendered := cfg.LogValue().String()

	assert.Contains(t, rendered, "dsn_configured=true")
	assert.NotContains(t, rendered, "secret-pass")
	assert.NotContains(t, rendered, "db.example.com")
	assert.NotContains(t, rendered, "dbname=app")
}

func TestConnect_RejectsEmptyDSN(t *testing.T) {
	_, err := Connect(context.Background(), Config{})
	require.Error(t, err)
}

func TestConnect_RejectsNilContext(t *testing.T) {
	var ctx context.Context
	_, err := Connect(ctx, Config{DSN: "postgres://user:pass@localhost:5432/db?sslmode=require"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestPool_InvalidReceiverSafety(t *testing.T) {
	for name, pool := range map[string]*Pool{
		"nil":  nil,
		"zero": &Pool{},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, pool.Pool())
			assert.NoError(t, pool.Close())

			assert.Error(t, pool.Ping(context.Background()))
			_, err := pool.Copy(context.Background(), "table", []string{"id"}, [][]any{{1}})
			assert.Error(t, err)

			ch, errCh, err := pool.Listen(context.Background(), "events")
			assert.Nil(t, ch)
			assert.Nil(t, errCh)
			assert.Error(t, err)

			assert.Error(t, pool.Notify(context.Background(), "events", "payload"))
		})
	}
}

func TestPool_CopyRejectsInvalidColumnsBeforePoolUse(t *testing.T) {
	pool := &Pool{pool: &pgxpool.Pool{}}

	_, err := pool.Copy(context.Background(), "items", []string{"secret-token"}, [][]any{{1}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid identifier")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestPool_CopyRejectsTooManyColumnsBeforePoolUse(t *testing.T) {
	pool := &Pool{pool: &pgxpool.Pool{}}
	columns := make([]string, MaxCopyColumns+1)
	for i := range columns {
		columns[i] = "id"
	}

	_, err := pool.Copy(context.Background(), "items", columns, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "column count")
}

func TestPool_CopyRejectsTooManyRowsBeforePoolUse(t *testing.T) {
	pool := &Pool{pool: &pgxpool.Pool{}}
	rows := make([][]any, MaxCopyRows+1)
	for i := range rows {
		rows[i] = []any{i}
	}

	_, err := pool.Copy(context.Background(), "items", []string{"id"}, rows)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "row count")
}

func TestPool_CopyRejectsRowWidthMismatchBeforePoolUse(t *testing.T) {
	pool := &Pool{pool: &pgxpool.Pool{}}

	_, err := pool.Copy(context.Background(), "items", []string{"id", "name"}, [][]any{{1}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "row width")
}

func TestListenCleanupContextPreservesValuesAfterCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), listenContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	cleanupCtx, cleanupCancel := listenCleanupContext(ctx, time.Second)
	defer cleanupCancel()

	assert.Equal(t, "trace-123", cleanupCtx.Value(listenContextKey{}))
	assert.NoError(t, cleanupCtx.Err())
}

func TestPool_ListenRejectsNilContext(t *testing.T) {
	pool := &Pool{pool: &pgxpool.Pool{}}
	var ctx context.Context
	ch, errCh, err := pool.Listen(ctx, "events")
	assert.Nil(t, ch)
	assert.Nil(t, errCh)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func parseForTest(t *testing.T, dsn string) *pgxpool.Config {
	t.Helper()
	pcfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	return pcfg
}

// checkTLS is a tiny helper around requireTLSOnParsedConfig that
// also passes the DSN so the function can read the raw sslmode (pgx
// maps require + verify-ca to identical TLSConfig fields, so the
// raw DSN is the only way to distinguish them).
func checkTLS(t *testing.T, dsn string, allowRequire bool) error {
	t.Helper()
	pcfg := parseForTest(t, dsn)
	return requireTLSOnParsedConfig(pcfg, dsn, allowRequire)
}

// requireTLSOnParsedConfig delegates to pgxpool's parser, so the unit
// tests below assert behaviour through the same path Connect uses.

func TestRequireTLS_AcceptsVerifyModes(t *testing.T) {
	// FR-079 [HIGH]: only verify-ca / verify-full are accepted by
	// default. require admits MITM and now needs explicit opt-in.
	for _, mode := range []string{"verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			err := checkTLS(t, "postgres://u:p@h/db?sslmode="+mode, false)
			assert.NoError(t, err, "sslmode=%s must be accepted", mode)
		})
	}
}

func TestRequireTLS_RejectsRequireByDefault(t *testing.T) {
	// FR-079 [HIGH]: sslmode=require encrypts but does NOT verify the
	// server's identity. A network attacker with any cert can MITM.
	// Pre-fix, require was on the accepted list.
	err := checkTLS(t, "postgres://u:p@h/db?sslmode=require", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sslmode=require admits MITM")
}

func TestRequireTLS_AcceptsRequireWithExplicitOptIn(t *testing.T) {
	err := checkTLS(t, "postgres://u:p@h/db?sslmode=require", true)
	assert.NoError(t, err, "sslmode=require must be accepted when allowRequire=true")
}

func TestRequireTLS_RejectsLooseModes(t *testing.T) {
	for _, mode := range []string{"prefer", "allow", "disable"} {
		t.Run(mode, func(t *testing.T) {
			err := checkTLS(t, "postgres://u:p@h/db?sslmode="+mode, false)
			assert.Error(t, err, "sslmode=%s must be rejected (admits plaintext)", mode)
		})
	}
}

func TestRequireTLS_RejectsLooseModesEvenWithAllowRequire(t *testing.T) {
	// AllowSSLModeRequire is a narrow opt-out for `require` only. It
	// must NOT also unlock prefer/allow/disable.
	for _, mode := range []string{"prefer", "allow", "disable"} {
		t.Run(mode, func(t *testing.T) {
			err := checkTLS(t, "postgres://u:p@h/db?sslmode="+mode, true)
			assert.Error(t, err, "sslmode=%s must STILL be rejected even with AllowSSLModeRequire", mode)
		})
	}
}

func TestRequireTLS_RejectsMissing(t *testing.T) {
	// pgxpool defaults sslmode to "prefer" when unset — which is itself
	// a plaintext-admitting mode and must be rejected.
	err := checkTLS(t, "postgres://u:p@h/db", false)
	assert.Error(t, err)
}

// TestRequireTLS_RejectsLastWinsBypass is the regression test for the
// N-3 audit finding: the previous extractSSLMode returned the FIRST
// sslmode= token while pgxpool honours the LAST. The lastSSLMode
// helper now also honours last-wins so the DSN-string parse and pgx's
// fallback parse stay aligned.
func TestRequireTLS_RejectsLastWinsBypass(t *testing.T) {
	err := checkTLS(t, "host=h user=u dbname=db sslmode=require sslmode=disable", true)
	assert.Error(t, err, "DSN with sslmode=disable as the last token must be rejected, regardless of earlier sslmode=require")
}

func TestLastSSLMode(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://u:p@h/db?sslmode=verify-full", "verify-full"},
		{"postgres://u:p@h/db?sslmode=require&host=h2", "require"},
		{"host=h sslmode=verify-ca", "verify-ca"},
		{"host=h sslmode=require sslmode=verify-full", "verify-full"},
		{"postgres://u:p@h/db", ""},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, lastSSLMode(c.dsn), "lastSSLMode(%q)", c.dsn)
	}
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
	for _, host := range []string{"10.0.0.5", "8.8.8.8", "evil.com", "0.0.0.0", "192.168.1.1", "secret-token.example"} {
		t.Run(host, func(t *testing.T) {
			err := requireLoopbackHost(host)
			assert.Error(t, err, "host %q must be rejected", host)
			assert.NotContains(t, err.Error(), host)
			assert.NotContains(t, err.Error(), "secret-token")
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
		{name: "semicolon", in: "public.users;drop", wantErr: true},
		{name: "space", in: "public.user table", wantErr: true},
		{name: "starts digit", in: "public.1users", wantErr: true},
		{name: "too long", in: strings.Repeat("x", maxCopyIdentifierBytes+1), wantErr: true},
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

func TestParseCopyIdentifier_ErrorDoesNotEchoTableName(t *testing.T) {
	_, err := parseCopyIdentifier("secret-token.schema.table")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
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
