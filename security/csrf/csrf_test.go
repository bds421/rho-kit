package csrf

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestIssuer(t *testing.T, opts ...Option) *Issuer {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	i, err := NewIssuer(secret, opts...)
	require.NoError(t, err)
	return i
}

func TestNewIssuer_RejectsShortSecret(t *testing.T) {
	_, err := NewIssuer(make([]byte, 16))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")
	assert.NotContains(t, err.Error(), "16")
}

func TestNewIssuer_RejectsNonPositiveTTL(t *testing.T) {
	_, err := NewIssuer(make([]byte, 32), WithTTL(0))
	require.Error(t, err)
	assert.EqualError(t, err, "csrf: TTL must be positive")
	assert.NotContains(t, err.Error(), "0s")

	_, err = NewIssuer(make([]byte, 32), WithTTL(-time.Second))
	require.Error(t, err)
	assert.EqualError(t, err, "csrf: TTL must be positive")
	assert.NotContains(t, err.Error(), "-1s")
}

func TestIssueAndVerify_RoundTrip(t *testing.T) {
	i := newTestIssuer(t)
	tok, err := i.Issue("user-42")
	require.NoError(t, err)
	require.NoError(t, i.Verify(tok, "user-42"))
}

func TestIssuer_VerifiesPreviousSecretDuringRotation(t *testing.T) {
	oldSecret := make([]byte, 32)
	newSecret := make([]byte, 32)
	for i := range oldSecret {
		oldSecret[i] = byte(i + 1)
		newSecret[i] = byte(i + 101)
	}

	oldIssuer, err := NewIssuer(oldSecret)
	require.NoError(t, err)
	oldToken, err := oldIssuer.Issue("user-42")
	require.NoError(t, err)

	rotated, err := NewIssuerWithSecrets(newSecret, [][]byte{oldSecret})
	require.NoError(t, err)
	require.NoError(t, rotated.Verify(oldToken, "user-42"))

	newToken, err := rotated.Issue("user-42")
	require.NoError(t, err)
	require.ErrorIs(t, oldIssuer.Verify(newToken, "user-42"), ErrTokenInvalid)
}

func TestIssue_RejectsInvalidSessionID(t *testing.T) {
	i := newTestIssuer(t)
	for name, sessionID := range invalidSessionIDs() {
		t.Run(name, func(t *testing.T) {
			_, err := i.Issue(sessionID)
			assert.ErrorIs(t, err, ErrSessionInvalid)
			if name == "over max" {
				assert.NotContains(t, err.Error(), "1024")
				assert.NotContains(t, err.Error(), "1025")
			}
		})
	}
}

func TestVerify_RejectsInvalidSessionID(t *testing.T) {
	i := newTestIssuer(t)
	tok, err := i.Issue("user-42")
	require.NoError(t, err)

	for name, sessionID := range invalidSessionIDs() {
		t.Run(name, func(t *testing.T) {
			err := i.Verify(tok, sessionID)
			assert.ErrorIs(t, err, ErrSessionInvalid)
			if name == "over max" {
				assert.NotContains(t, err.Error(), "1024")
				assert.NotContains(t, err.Error(), "1025")
			}
		})
	}
}

func TestValidateSessionID_AcceptsCommonOpaqueIDs(t *testing.T) {
	for _, sessionID := range []string{
		"user-42",
		"550e8400-e29b-41d4-a716-446655440000",
		"v4.public.eyJzdWIiOiJ1c2VyIn0",
		"base64url_token-abc_123",
	} {
		t.Run(sessionID, func(t *testing.T) {
			assert.NoError(t, ValidateSessionID(sessionID))
		})
	}
}

func TestIssue_FreshNonceEachCall(t *testing.T) {
	i := newTestIssuer(t)
	tokens := make(map[Token]struct{})
	for range 100 {
		tok, err := i.Issue("user")
		require.NoError(t, err)
		_, dup := tokens[tok]
		assert.False(t, dup, "tokens must be unique even for the same session")
		tokens[tok] = struct{}{}
	}
}

func TestVerify_TamperedTokenRejected(t *testing.T) {
	i := newTestIssuer(t)
	tok, err := i.Issue("user")
	require.NoError(t, err)

	// Flip a byte in the middle.
	bad := []byte(tok)
	bad[len(bad)/2] ^= 0xFF
	err = i.Verify(Token(bad), "user")
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestVerify_WrongSessionRejected(t *testing.T) {
	// FR-022 [MED]: post-fix, the verifier checks the HMAC first. A
	// token minted for user-A presented as user-B has both a wrong
	// MAC (because computeMAC binds sessionID) AND a wrong session
	// prefix; the verifier returns the conservative ErrTokenInvalid
	// rather than ErrSessionMismatch so it does not act as a
	// session-prefix oracle. ErrSessionMismatch is reserved for the
	// pathological case where a caller has somehow produced a token
	// whose MAC matches but whose prefix does not (only reachable by
	// an attacker already in possession of the secret).
	i := newTestIssuer(t)
	tok, err := i.Issue("user-A")
	require.NoError(t, err)

	err = i.Verify(tok, "user-B")
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestVerify_ExpiredTokenRejected(t *testing.T) {
	now := time.Now()
	i := newTestIssuer(t,
		WithTTL(time.Hour),
		WithClock(func() time.Time { return now }),
	)
	tok, err := i.Issue("user")
	require.NoError(t, err)

	// Advance clock past TTL.
	now = now.Add(2 * time.Hour)
	err = i.Verify(tok, "user")
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestVerify_FutureTokenRejected(t *testing.T) {
	now := time.Now()
	i := newTestIssuer(t,
		WithClock(func() time.Time { return now }),
	)
	tok, err := i.Issue("user")
	require.NoError(t, err)

	// Move local clock backwards by 5 minutes — the token's iat is "in
	// the future" relative to local clock by more than the 60s skew
	// budget, so Verify must reject.
	now = now.Add(-5 * time.Minute)
	err = i.Verify(tok, "user")
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestVerify_MalformedRejected(t *testing.T) {
	i := newTestIssuer(t)
	cases := []Token{
		"",                               // empty
		"not-base64-!!@@##",              // bad base64
		"short",                          // too short
		Token(strings.Repeat("A", 1000)), // valid base64 but wrong size
	}
	for _, c := range cases {
		err := i.Verify(c, "user")
		assert.ErrorIs(t, err, ErrTokenInvalid)
	}
}

func TestOriginAllowlist_OriginPreferredOverReferer(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.True(t, a.Allowed("https://app.example.com", ""))
	assert.True(t, a.Allowed("https://app.example.com", "https://attacker.com/page"))
	assert.False(t, a.Allowed("https://attacker.com", "https://app.example.com/page"))
}

func TestOriginAllowlist_RefererFallback(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.True(t, a.Allowed("", "https://app.example.com/login"))
	assert.False(t, a.Allowed("", "https://attacker.com"))
}

func TestOriginAllowlist_EmptyRejectsAll(t *testing.T) {
	a := NewOriginAllowlist()
	assert.False(t, a.Allowed("https://app.example.com", ""))
}

func TestOriginAllowlist_NoOriginNorReferer(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.False(t, a.Allowed("", ""))
}

func TestOriginAllowlist_PortMatters(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com:8080")
	assert.True(t, a.Allowed("https://app.example.com:8080", ""))
	assert.False(t, a.Allowed("https://app.example.com", ""), "missing port must not match")
	assert.False(t, a.Allowed("https://app.example.com:443", ""), "different port must not match")
}

func TestOriginAllowlist_CaseInsensitiveSchemeHost(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.True(t, a.Allowed("HTTPS://APP.EXAMPLE.COM", ""))
}

func TestOriginAllowlist_StripsPathQuery(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.True(t, a.Allowed("https://app.example.com/some/path?x=1", ""))
}

func TestOriginAllowlist_PanicsOnInvalidConfiguredOrigin(t *testing.T) {
	cases := []string{
		"",
		" ",
		"ftp://app.example.com",
		"https://app.example.com/",
		"https://app.example.com/path",
		"https://app.example.com?x=1",
		"https://user@app.example.com",
		"https://app.example.com:bad",
		"https://app.example.com:+443",
		"https://[not-ip]:443",
		"https://app.example.com\n",
		string([]byte("https://app.example.com\xff")),
	}
	for _, origin := range cases {
		name := strings.ReplaceAll(origin, "/", "_")
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() {
				NewOriginAllowlist(origin)
			})
		})
	}
}

func TestOriginAllowlist_InvalidConfiguredOriginDoesNotEchoValue(t *testing.T) {
	assert.PanicsWithValue(t, "csrf: NewOriginAllowlist invalid origin allowlist entry", func() {
		NewOriginAllowlist("https://app.example.com/%zz?token=secret-token")
	})
}

func TestOriginAllowlist_RejectsMalformedRuntimeOrigin(t *testing.T) {
	a := NewOriginAllowlist("https://app.example.com")
	assert.False(t, a.Allowed("https://app.example.com:bad", ""))
	assert.False(t, a.Allowed("https://app.example.com:+443", ""))
	assert.False(t, a.Allowed("https://app.example.com@evil.example", ""))
	assert.False(t, a.Allowed("null", "https://app.example.com/path"))
	assert.False(t, a.Allowed("https://app.example.com\n", "https://app.example.com/path"))
	assert.False(t, a.Allowed(string([]byte("https://app.example.com\xff")), ""))
	assert.False(t, a.Allowed("", "https://app.example.com/path\n"))
	assert.False(t, a.Allowed("", string([]byte("https://app.example.com/path\xff"))))
	assert.False(t, a.Allowed("", "https://app.example.com/path with space"))
}

func TestOriginAllowlist_IPv6Origin(t *testing.T) {
	a := NewOriginAllowlist("https://[2001:db8::1]:8443")
	assert.True(t, a.Allowed("https://[2001:db8::1]:8443/path", ""))
	assert.False(t, a.Allowed("https://[2001:db8::1]", ""))
	assert.False(t, a.Allowed("https://[not-ip]:8443/path", ""))
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithClock(nil)
}

func TestNewIssuer_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = NewIssuer(make([]byte, 32), nil)
	})
}

func invalidSessionIDs() map[string]string {
	return map[string]string{
		"empty":           "",
		"only whitespace": " \t ",
		"leading space":   " user",
		"trailing space":  "user ",
		"embedded space":  "user 42",
		"tab":             "user\t42",
		"newline":         "user\n42",
		"null":            "user\x0042",
		"invalid utf8":    string([]byte{'u', 0xff}),
		"over max":        strings.Repeat("a", MaxSessionIDLen+1),
	}
}

func TestNewIssuer_RejectsSubSecondTTL(t *testing.T) {
	_, err := NewIssuer(make([]byte, 32), WithTTL(500*time.Millisecond))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1s")
}
