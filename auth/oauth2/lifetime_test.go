package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// ttlRecordingStore records the TTL of every write.
type ttlRecordingStore struct {
	*oauth2.MemorySessionStore
	mu   sync.Mutex
	ttls []time.Duration
}

func (s *ttlRecordingStore) Put(ctx context.Context, id string, sess oauth2.Session, ttl time.Duration) error {
	s.record(ttl)
	return s.MemorySessionStore.Put(ctx, id, sess, ttl)
}

func (s *ttlRecordingStore) Update(ctx context.Context, id string, v uint64, sess oauth2.Session, ttl time.Duration) (bool, error) {
	s.record(ttl)
	return s.MemorySessionStore.Update(ctx, id, v, sess, ttl)
}

func (s *ttlRecordingStore) record(ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttls = append(s.ttls, ttl)
}

func (s *ttlRecordingStore) lastTTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ttls) == 0 {
		return 0
	}
	return s.ttls[len(s.ttls)-1]
}

func newLifetimeClient(t *testing.T, store oauth2.SessionStore, opts ...oauth2.Option) (*fakeIssuer, *oauth2.Client) {
	t.Helper()
	issuer := newFakeIssuer(t, "test-client")
	opts = append([]oauth2.Option{
		oauth2.WithSessionStore(store),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	}, opts...)
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, opts...)
	require.NoError(t, err)
	return issuer, client
}

func seedSession(t *testing.T, store oauth2.SessionStore, sess oauth2.Session) {
	t.Helper()
	if sess.SessionID == "" {
		sess.SessionID = "s1"
	}
	if sess.UserID == "" {
		sess.UserID = "user-123"
	}
	if sess.AccessToken == nil {
		sess.AccessToken = secret.NewFromString("access-1")
	}
	require.NoError(t, store.Put(context.Background(), sess.SessionID, sess, time.Hour))
}

func sessionRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: id})
	return req
}

// runLogin drives login (with loginQuery) → provider → callback and returns
// the callback response.
func runLogin(t *testing.T, client *oauth2.Client, loginQuery string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/oauth/login"+loginQuery, nil))
	require.Equal(t, http.StatusFound, login.Code, login.Body.String())
	authURL, err := url.Parse(login.Header().Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	httpc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authResp, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	_ = authResp.Body.Close()

	cb := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	for _, ck := range login.Result().Cookies() {
		cb.AddCookie(ck)
	}
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, cb)
	return resp
}

func sessionCookieFrom(t *testing.T, resp *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range resp.Result().Cookies() {
		if ck.Name == "kit_oauth_session" && ck.Value != "" {
			return ck
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestAbsoluteLifetime_EndsSessionAtCap(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store, oauth2.WithAbsoluteSessionLifetime(time.Hour))

	seedSession(t, store, oauth2.Session{SessionID: "young", CreatedAt: time.Now().Add(-59 * time.Minute)})
	_, err := client.SessionFromRequest(context.Background(), sessionRequest("young"))
	require.NoError(t, err)

	seedSession(t, store, oauth2.Session{SessionID: "old", CreatedAt: time.Now().Add(-time.Hour)})
	_, err = client.SessionFromRequest(context.Background(), sessionRequest("old"))
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	_, err = store.Get(context.Background(), "old")
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound, "a capped session must be deleted")

	seedSession(t, store, oauth2.Session{SessionID: "legacy"})
	_, err = client.SessionFromRequest(context.Background(), sessionRequest("legacy"))
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound, "a session without CreatedAt fails closed")
}

func TestLifetimeOptions_DefaultUnchanged(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store)
	seedSession(t, store, oauth2.Session{})
	sess, err := client.SessionFromRequest(context.Background(), sessionRequest("s1"))
	require.NoError(t, err)
	assert.True(t, sess.LastActivity.IsZero(), "no activity is recorded without WithIdleTimeout")
	assert.Equal(t, uint64(0), sess.Version)
}

func TestAbsoluteLifetime_RefreshNeverExtendsBeyondCap(t *testing.T) {
	store := &ttlRecordingStore{MemorySessionStore: oauth2.NewMemorySessionStore()}
	issuer, client := newLifetimeClient(t, store,
		oauth2.WithAbsoluteSessionLifetime(time.Hour), oauth2.WithTokenRefresh(time.Minute))
	issuer.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		writeTokens(t, w, map[string]any{"access_token": "access-2", "token_type": "Bearer", "expires_in": 7200})
	}
	seedSession(t, store, oauth2.Session{
		RefreshToken: secret.NewFromString("refresh-1"), Expiry: time.Now().Add(-time.Minute),
		CreatedAt: time.Now().Add(-50 * time.Minute),
	})
	sess, err := client.SessionFromRequest(context.Background(), sessionRequest("s1"))
	require.NoError(t, err)
	assert.Equal(t, "access-2", sess.AccessToken.RevealString())
	ttl := store.lastTTL()
	assert.LessOrEqual(t, ttl, 10*time.Minute, "refresh must not extend beyond CreatedAt+lifetime")
	assert.Greater(t, ttl, 9*time.Minute)
}

func TestAbsoluteLifetime_CallbackStampsCreatedAtAndCapsCookie(t *testing.T) {
	store := &ttlRecordingStore{MemorySessionStore: oauth2.NewMemorySessionStore()}
	_, client := newLifetimeClient(t, store, oauth2.WithAbsoluteSessionLifetime(2*time.Hour))
	resp := runLogin(t, client, "")
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	ck := sessionCookieFrom(t, resp)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), ck.Expires, 5*time.Second)
	assert.LessOrEqual(t, store.lastTTL(), 2*time.Hour)

	sess, err := store.Get(context.Background(), ck.Value)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), sess.CreatedAt, 5*time.Second)
	assert.Equal(t, sess.CreatedAt, sess.LastActivity)
}

func TestIdleTimeout_EndsInactiveSession(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store, oauth2.WithIdleTimeout(30*time.Minute))

	seedSession(t, store, oauth2.Session{CreatedAt: time.Now().Add(-2 * time.Hour), LastActivity: time.Now().Add(-30 * time.Minute)})
	_, err := client.SessionFromRequest(context.Background(), sessionRequest("s1"))
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	_, err = store.Get(context.Background(), "s1")
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)

	// LastActivity absent: CreatedAt is the reference.
	seedSession(t, store, oauth2.Session{SessionID: "s2", CreatedAt: time.Now().Add(-10 * time.Minute)})
	_, err = client.SessionFromRequest(context.Background(), sessionRequest("s2"))
	require.NoError(t, err)
}

func TestIdleTimeout_UserRequestRecordsActivityBackgroundDoesNot(t *testing.T) {
	store := &ttlRecordingStore{MemorySessionStore: oauth2.NewMemorySessionStore()}
	_, client := newLifetimeClient(t, store, oauth2.WithIdleTimeout(30*time.Minute))
	stale := time.Now().Add(-5 * time.Minute)
	seedSession(t, store, oauth2.Session{CreatedAt: stale, LastActivity: stale})
	ctx := context.Background()

	// Header marker.
	bg := sessionRequest("s1")
	bg.Header.Set(oauth2.DefaultBackgroundRequestHeader, "1")
	_, err := client.SessionFromRequest(ctx, bg)
	require.NoError(t, err)
	stored, _ := store.Get(ctx, "s1")
	assert.Equal(t, stale.Unix(), stored.LastActivity.Unix(), "background header must not record activity")

	// Context marker.
	_, err = client.SessionFromRequest(oauth2.WithBackgroundRequest(ctx), sessionRequest("s1"))
	require.NoError(t, err)
	stored, _ = store.Get(ctx, "s1")
	assert.Equal(t, stale.Unix(), stored.LastActivity.Unix(), "background context must not record activity")

	// User request records activity and caps the store TTL at the idle window.
	sess, err := client.SessionFromRequest(ctx, sessionRequest("s1"))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), sess.LastActivity, 5*time.Second)
	stored, _ = store.Get(ctx, "s1")
	assert.WithinDuration(t, time.Now(), stored.LastActivity, 5*time.Second)
	assert.Equal(t, uint64(1), stored.Version)
	assert.LessOrEqual(t, store.lastTTL(), 30*time.Minute)

	// Within a minute of the last write no further write happens.
	writes := len(store.ttls)
	_, err = client.SessionFromRequest(ctx, sessionRequest("s1"))
	require.NoError(t, err)
	assert.Len(t, store.ttls, writes, "activity is written at most once per minute")
}

func TestIdleTimeout_CustomBackgroundHeader(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store, oauth2.WithIdleTimeout(time.Hour), oauth2.WithBackgroundRequestHeader("x-poll"))
	stale := time.Now().Add(-5 * time.Minute)
	seedSession(t, store, oauth2.Session{CreatedAt: stale, LastActivity: stale})
	req := sessionRequest("s1")
	req.Header.Set("X-Poll", "TRUE")
	_, err := client.SessionFromRequest(context.Background(), req)
	require.NoError(t, err)
	stored, _ := store.Get(context.Background(), "s1")
	assert.Equal(t, stale.Unix(), stored.LastActivity.Unix())

	// The default header name is no longer recognised.
	req = sessionRequest("s1")
	req.Header.Set(oauth2.DefaultBackgroundRequestHeader, "1")
	_, err = client.SessionFromRequest(context.Background(), req)
	require.NoError(t, err)
	stored, _ = store.Get(context.Background(), "s1")
	assert.WithinDuration(t, time.Now(), stored.LastActivity, 5*time.Second)
}

func TestIdleTimeout_RefreshKeepsConcurrentActivity(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	stale := time.Now().Add(-5 * time.Minute)
	issuer, client := newLifetimeClient(t, store, oauth2.WithIdleTimeout(time.Hour), oauth2.WithTokenRefresh(time.Minute))
	touched := time.Now().Add(-time.Second).Truncate(time.Second)
	issuer.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		// Another replica records activity while the refresh is in flight.
		cur, err := store.Get(context.Background(), "s1")
		require.NoError(t, err)
		cur.LastActivity = touched
		cur.Version++
		ok, err := store.Update(context.Background(), "s1", cur.Version-1, cur, time.Hour)
		require.NoError(t, err)
		require.True(t, ok)
		writeTokens(t, w, map[string]any{"access_token": "access-2", "token_type": "Bearer", "expires_in": 3600})
	}
	seedSession(t, store, oauth2.Session{
		RefreshToken: secret.NewFromString("refresh-1"), Expiry: time.Now().Add(-time.Minute),
		CreatedAt: stale, LastActivity: stale,
	})
	req := sessionRequest("s1")
	req.Header.Set(oauth2.DefaultBackgroundRequestHeader, "1")
	sess, err := client.SessionFromRequest(context.Background(), req)
	require.NoError(t, err, "an activity-only write must not discard refreshed tokens")
	assert.Equal(t, "access-2", sess.AccessToken.RevealString())
	stored, err := store.Get(context.Background(), "s1")
	require.NoError(t, err)
	assert.Equal(t, "access-2", stored.AccessToken.RevealString())
	assert.Equal(t, touched.Unix(), stored.LastActivity.Unix())
}

func TestLifetimeOptions_PanicOnInvalidValues(t *testing.T) {
	assert.Panics(t, func() { oauth2.WithAbsoluteSessionLifetime(0) })
	assert.Panics(t, func() { oauth2.WithIdleTimeout(-time.Second) })
	assert.Panics(t, func() { oauth2.WithBackgroundRequestHeader("") })
	assert.Panics(t, func() { oauth2.WithBackgroundRequestHeader("bad header") })
}
