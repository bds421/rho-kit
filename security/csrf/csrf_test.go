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
}

func TestNewIssuer_RejectsNonPositiveTTL(t *testing.T) {
	_, err := NewIssuer(make([]byte, 32), WithTTL(0))
	require.Error(t, err)
	_, err = NewIssuer(make([]byte, 32), WithTTL(-time.Second))
	require.Error(t, err)
}

func TestIssueAndVerify_RoundTrip(t *testing.T) {
	i := newTestIssuer(t)
	tok, err := i.Issue("user-42")
	require.NoError(t, err)
	require.NoError(t, i.Verify(tok, "user-42"))
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

func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithClock(nil)
}
