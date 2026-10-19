package oauth2_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

const logoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// signLogoutToken signs a logout token with valid defaults; a nil value in
// overrides removes that claim.
func signLogoutToken(t *testing.T, f *fakeIssuer, key *rsa.PrivateKey, overrides map[string]any) string {
	t.Helper()
	if key == nil {
		key = f.signingKey
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("logout+jwt").WithHeader("kid", "test-key-1"),
	)
	require.NoError(t, err)
	claims := map[string]any{
		"iss":    f.server.URL,
		"aud":    f.clientID,
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(2 * time.Minute).Unix(),
		"jti":    rand.Text(),
		"events": map[string]any{logoutEvent: map[string]any{}},
		"sub":    "user-123",
		"sid":    "idp-session-1",
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

type backChannelFixture struct {
	issuer   *fakeIssuer
	sessions *oauth2.MemorySessionStore
	client   *oauth2.Client
	mu       sync.Mutex
	revoked  []string
}

func newBackChannelFixture(t *testing.T) *backChannelFixture {
	t.Helper()
	f := &backChannelFixture{issuer: newFakeIssuer(t, "test-client"), sessions: oauth2.NewMemorySessionStore()}
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: f.issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	},
		oauth2.WithSessionStore(f.sessions),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithBackChannelLogout(),
		oauth2.WithSessionRevocationHook(func(_ context.Context, subject string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.revoked = append(f.revoked, subject)
		}),
	)
	require.NoError(t, err)
	f.client = client
	for _, s := range []oauth2.Session{
		{SessionID: "a1", UserID: "user-123", SID: "idp-session-1"},
		{SessionID: "a2", UserID: "user-123", SID: "idp-session-2"},
		{SessionID: "b1", UserID: "user-456", SID: "idp-session-3"},
	} {
		seedSession(t, f.sessions, s)
	}
	return f
}

func (f *backChannelFixture) post(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/backchannel-logout",
		strings.NewReader(url.Values{"logout_token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	f.client.Handlers().ServeHTTP(res, req)
	return res
}

func (f *backChannelFixture) live(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, id := range []string{"a1", "a2", "b1"} {
		if _, err := f.sessions.Get(context.Background(), id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func TestBackChannelLogout_SIDDeletesOnlyThatProviderSession(t *testing.T) {
	f := newBackChannelFixture(t)
	res := f.post(t, signLogoutToken(t, f.issuer, nil, nil))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Equal(t, "no-store", res.Header().Get("Cache-Control"))
	assert.Equal(t, []string{"a2", "b1"}, f.live(t))
	assert.Equal(t, []string{"user-123"}, f.revoked)
}

func TestBackChannelLogout_SubDeletesAllSessionsOfSubject(t *testing.T) {
	f := newBackChannelFixture(t)
	res := f.post(t, signLogoutToken(t, f.issuer, nil, map[string]any{"sid": nil}))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	assert.Equal(t, []string{"b1"}, f.live(t))
	assert.Equal(t, []string{"user-123"}, f.revoked)
}

func TestBackChannelLogout_SIDWithMismatchedSubDeletesNothing(t *testing.T) {
	f := newBackChannelFixture(t)
	res := f.post(t, signLogoutToken(t, f.issuer, nil, map[string]any{"sub": "user-456"}))
	require.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, []string{"a1", "a2", "b1"}, f.live(t))
}

func TestBackChannelLogout_SIDOnlyNotifiesSessionSubject(t *testing.T) {
	f := newBackChannelFixture(t)
	res := f.post(t, signLogoutToken(t, f.issuer, nil, map[string]any{"sub": nil, "sid": "idp-session-3"}))
	require.Equal(t, http.StatusOK, res.Code)
	assert.Equal(t, []string{"a1", "a2"}, f.live(t))
	assert.Equal(t, []string{"user-456"}, f.revoked)
}

func TestBackChannelLogout_ReplayedJTIRejected(t *testing.T) {
	f := newBackChannelFixture(t)
	token := signLogoutToken(t, f.issuer, nil, map[string]any{"jti": "fixed-jti"})
	require.Equal(t, http.StatusOK, f.post(t, token).Code)
	// A fresh session for the same provider session after the first logout.
	seedSession(t, f.sessions, oauth2.Session{SessionID: "a1", UserID: "user-123", SID: "idp-session-1"})
	res := f.post(t, token)
	assert.Equal(t, http.StatusBadRequest, res.Code)
	assert.JSONEq(t, `{"error":"invalid_request"}`, res.Body.String())
	assert.Contains(t, f.live(t), "a1", "a replayed token must not delete sessions")
}

func TestBackChannelLogout_RejectsInvalidTokens(t *testing.T) {
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cases := map[string]struct {
		key       *rsa.PrivateKey
		overrides map[string]any
	}{
		"wrong audience":        {overrides: map[string]any{"aud": "other-client"}},
		"wrong issuer":          {overrides: map[string]any{"iss": "https://evil.example"}},
		"bad signature":         {key: otherKey},
		"expired":               {overrides: map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}},
		"missing events":        {overrides: map[string]any{"events": nil}},
		"events without logout": {overrides: map[string]any{"events": map[string]any{"urn:other": map[string]any{}}}},
		"event not an object":   {overrides: map[string]any{"events": map[string]any{logoutEvent: "yes"}}},
		"nonce present":         {overrides: map[string]any{"nonce": "n"}},
		"no sub and no sid":     {overrides: map[string]any{"sub": nil, "sid": nil}},
		"missing jti":           {overrides: map[string]any{"jti": nil}},
		"missing iat":           {overrides: map[string]any{"iat": nil}},
		"iat too old":           {overrides: map[string]any{"iat": time.Now().Add(-10 * time.Minute).Unix()}},
		"iat in the future":     {overrides: map[string]any{"iat": time.Now().Add(10 * time.Minute).Unix()}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBackChannelFixture(t)
			res := f.post(t, signLogoutToken(t, f.issuer, tc.key, tc.overrides))
			assert.Equal(t, http.StatusBadRequest, res.Code)
			assert.Equal(t, []string{"a1", "a2", "b1"}, f.live(t))
			assert.Empty(t, f.revoked)
		})
	}
}

func TestBackChannelLogout_RequestShape(t *testing.T) {
	f := newBackChannelFixture(t)
	assert.Equal(t, http.StatusBadRequest, f.post(t, "").Code, "missing logout_token")
	assert.Equal(t, http.StatusBadRequest, f.post(t, "not-a-jwt").Code)
	res := httptest.NewRecorder()
	f.client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/backchannel-logout", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, res.Code)
}

func TestBackChannelLogout_DisabledByDefault(t *testing.T) {
	_, client := newLifetimeClient(t, oauth2.NewMemorySessionStore())
	res := httptest.NewRecorder()
	client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/oauth/backchannel-logout", nil))
	assert.Equal(t, http.StatusNotFound, res.Code)
}

func TestBackChannelLogout_RequiresIndexedStore(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	_, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, oauth2.WithSessionStore(nonAtomicStore{inner: oauth2.NewMemorySessionStore()}),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()), oauth2.WithBackChannelLogout())
	require.ErrorContains(t, err, "SessionIndex")
}

func TestDeleteSessionsBySubject(t *testing.T) {
	f := newBackChannelFixture(t)
	n, err := f.client.DeleteSessionsBySubject(context.Background(), "user-123")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"b1"}, f.live(t))
	assert.Equal(t, []string{"user-123"}, f.revoked)

	n, err = f.client.DeleteSessionsBySubject(context.Background(), "nobody")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, []string{"user-123", "nobody"}, f.revoked, "the hook runs even without stored sessions")

	_, err = f.client.DeleteSessionsBySubject(context.Background(), "")
	require.Error(t, err)

	issuer := newFakeIssuer(t, "test-client")
	plain, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, oauth2.WithSessionStore(nonAtomicStore{inner: oauth2.NewMemorySessionStore()}), oauth2.WithStateStore(oauth2.NewMemoryStateStore()))
	require.NoError(t, err)
	_, err = plain.DeleteSessionsBySubject(context.Background(), "user-123")
	require.ErrorIs(t, err, oauth2.ErrSessionIndexUnsupported)
}

func TestCallback_StoresProviderSID(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	issuer, client := newLifetimeClient(t, store)
	issuer.idTokenExtra = map[string]any{"sid": "idp-session-9"}
	resp := runLogin(t, client, "")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	sess, err := store.Get(context.Background(), sessionCookieFrom(t, resp).Value)
	require.NoError(t, err)
	assert.Equal(t, "idp-session-9", sess.SID)
	ids, err := store.SessionIDsBySID(context.Background(), "idp-session-9")
	require.NoError(t, err)
	assert.Equal(t, []string{sess.SessionID}, ids)
}

func TestMemorySessionStore_IndexConsistency(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	ctx := context.Background()
	lookup := func(bySID bool, key string) []string {
		var ids []string
		var err error
		if bySID {
			ids, err = store.SessionIDsBySID(ctx, key)
		} else {
			ids, err = store.SessionIDsBySubject(ctx, key)
		}
		require.NoError(t, err)
		sort.Strings(ids)
		return ids
	}
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{UserID: "u1", SID: "sid-a"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s2", oauth2.Session{UserID: "u1"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s3", oauth2.Session{UserID: "u2", SID: "sid-b"}, time.Millisecond))
	assert.Equal(t, []string{"s1", "s2"}, lookup(false, "u1"))
	assert.Equal(t, []string{"s1"}, lookup(true, "sid-a"))

	// Update moves the SID index entry.
	ok, err := store.Update(ctx, "s1", 0, oauth2.Session{UserID: "u1", SID: "sid-c", Version: 1}, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, lookup(true, "sid-a"))
	assert.Equal(t, []string{"s1"}, lookup(true, "sid-c"))

	// Delete removes from both indexes.
	require.NoError(t, store.Delete(ctx, "s1"))
	assert.Equal(t, []string{"s2"}, lookup(false, "u1"))
	assert.Empty(t, lookup(true, "sid-c"))

	// Expired sessions are omitted and swept from the index.
	time.Sleep(5 * time.Millisecond)
	assert.Empty(t, lookup(false, "u2"))
	require.NoError(t, store.Put(ctx, "s4", oauth2.Session{UserID: "u3"}, time.Hour))
	assert.Empty(t, lookup(true, "sid-b"))
}

func TestMemoryReplayCache(t *testing.T) {
	cache := oauth2.NewMemoryReplayCache()
	ctx := context.Background()
	first, err := cache.Remember(ctx, "k", 20*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, first)
	first, err = cache.Remember(ctx, "k", time.Minute)
	require.NoError(t, err)
	assert.False(t, first)
	time.Sleep(30 * time.Millisecond)
	first, err = cache.Remember(ctx, "k", time.Minute)
	require.NoError(t, err)
	assert.True(t, first, "an expired key may be recorded again")
	_, err = cache.Remember(ctx, "", time.Minute)
	require.Error(t, err)
}
