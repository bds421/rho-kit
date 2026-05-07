package signedrequest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	keyID     = "test-key"
	secretStr = "0123456789abcdef0123456789abcdef"
)

func newResolver(t *testing.T) KeyResolver {
	t.Helper()
	return func(id string) ([]byte, error) {
		require.Equal(t, keyID, id)
		return []byte(secretStr), nil
	}
}

// signRequest helper produces a verifier-acceptable request via the
// public SignCanonical so the test exercises the same canonical-string
// builder as production.
func signRequest(t *testing.T, method, target, body string, ts time.Time, nonce string, requiredHeaders []string, extraHeaders map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	tsUnix := ts.UTC().Unix()
	req.Header.Set(HeaderTimestamp, formatUnix(tsUnix))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderKeyID, keyID)
	req.Header.Set(HeaderSignature, SignCanonical([]byte(secretStr), req, formatUnix(tsUnix), nonce, []byte(body), requiredHeaders))
	// Re-attach the body so the middleware can read it. SignCanonical
	// did not consume; httptest.NewRequest already wraps the reader.
	if body != "" {
		req.Body = http.NoBody
		req.Body = newBody(body)
		req.ContentLength = int64(len(body))
	}
	return req
}

func newBody(s string) *fakeReadCloser { return &fakeReadCloser{Reader: bytes.NewReader([]byte(s))} }

type fakeReadCloser struct{ *bytes.Reader }

func (f *fakeReadCloser) Close() error { return nil }

func formatUnix(t int64) string { return strconv.FormatInt(t, 10) }

func TestVerify_RoundTrip(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := signRequest(t, "POST", "/api/x", "hello", time.Now(), "nonce-1", nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestVerify_RejectsReplayedNonce(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	now := time.Now()
	r1 := signRequest(t, "POST", "/x", "body", now, "same-nonce", nil, nil)
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, r1)
	require.Equal(t, http.StatusOK, rr1.Code)

	r2 := signRequest(t, "POST", "/x", "body", now, "same-nonce", nil, nil)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, r2)
	assert.Equal(t, http.StatusUnauthorized, rr2.Code)
}

func TestVerify_RejectsExpiredTimestamp(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithMaxClockSkew(time.Minute))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	old := time.Now().Add(-time.Hour)
	r := signRequest(t, "POST", "/x", "", old, "nonce-old", nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerify_RejectsModifiedBody(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "original", time.Now(), "nonce-mod", nil, nil)
	// Tamper after signing.
	r.Body = newBody("tampered")
	r.ContentLength = int64(len("tampered"))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestVerify_RejectsMissingHeaders(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := httptest.NewRequest("GET", "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerify_RequiredHeaderEnforced(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithRequiredHeaders("X-Tenant-ID"))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	// With required header — round-trip succeeds.
	rOK := signRequest(t, "POST", "/x", "", time.Now(), "n1", []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
	rrOK := httptest.NewRecorder()
	h.ServeHTTP(rrOK, rOK)
	assert.Equal(t, http.StatusOK, rrOK.Code)

	// Same signing, but the header is dropped after signing → MAC mismatch.
	rBad := signRequest(t, "POST", "/x", "", time.Now(), "n2", []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
	rBad.Header.Del("X-Tenant-ID")
	rrBad := httptest.NewRecorder()
	h.ServeHTTP(rrBad, rBad)
	assert.Equal(t, http.StatusBadRequest, rrBad.Code)
}

func TestMiddleware_PanicsWithoutNonceStore(t *testing.T) {
	assert.Panics(t, func() {
		Middleware(newResolver(t), nil)
	})
}

func TestMiddleware_PanicsWithoutResolver(t *testing.T) {
	assert.Panics(t, func() {
		Middleware(nil, NewMemoryNonceStore(time.Minute))
	})
}

func TestVerify_RejectsShortResolvedSecret(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	short := bytes.Repeat([]byte("a"), 31)
	resolver := func(string) ([]byte, error) { return short, nil }
	mw := Middleware(resolver, store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "body", time.Now(), "nonce-short", nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestVerify_AcceptsExactly32ByteResolvedSecret(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	exact := bytes.Repeat([]byte("a"), 32)
	resolver := func(string) ([]byte, error) { return exact, nil }
	mw := Middleware(resolver, store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	tsUnix := time.Now().UTC().Unix()
	body := "hello"
	req := httptest.NewRequest("POST", "/api/x", strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, formatUnix(tsUnix))
	req.Header.Set(HeaderNonce, "n-32-exact")
	req.Header.Set(HeaderKeyID, keyID)
	req.Header.Set(HeaderSignature, SignCanonical(exact, req, formatUnix(tsUnix), "n-32-exact", []byte(body), nil))
	req.Body = newBody(body)
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWithMaxClockSkew_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithMaxClockSkew(0) })
	assert.Panics(t, func() { WithMaxClockSkew(-time.Second) })
}

func TestWithBodyMaxSize_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithBodyMaxSize(0) })
	assert.Panics(t, func() { WithBodyMaxSize(-1) })
}

func TestMemoryNonceStore_Sweep(t *testing.T) {
	now := time.Now()
	s := NewMemoryNonceStore(time.Second)
	s.now = func() time.Time { return now }

	first, _ := s.SeenOrStore("a")
	require.True(t, first)

	// Same instant → replay.
	second, _ := s.SeenOrStore("a")
	require.False(t, second)

	// Past TTL → first-time again.
	now = now.Add(2 * time.Second)
	third, _ := s.SeenOrStore("a")
	assert.True(t, third)
}

func TestMemoryNonceStore_WithSweepEvery_Immediate(t *testing.T) {
	// sweepEvery=1 means every call triggers a sweep, so an entry
	// older than TTL is reclaimed on the very next SeenOrStore.
	now := time.Now()
	s := NewMemoryNonceStore(time.Second, WithSweepEvery(1))
	s.now = func() time.Time { return now }

	first, _ := s.SeenOrStore("a")
	require.True(t, first)
	require.Equal(t, 1, s.Len())

	// Advance past TTL and probe a *different* nonce. With sweepEvery=1
	// the sweep runs on this call and "a" is reclaimed before the
	// probe of "b" inserts.
	now = now.Add(2 * time.Second)
	first, _ = s.SeenOrStore("b")
	require.True(t, first)
	assert.Equal(t, 1, s.Len(), "stale 'a' must be swept; only 'b' remains")
}

func TestMemoryNonceStore_WithSweepEvery_Deferred(t *testing.T) {
	// A large sweepEvery means the map keeps stale entries until the
	// cadence is reached. Verify the entry stays in the map after TTL
	// has elapsed but before the sweep cadence fires.
	now := time.Now()
	s := NewMemoryNonceStore(time.Second, WithSweepEvery(1_000_000))
	s.now = func() time.Time { return now }

	for i := 0; i < 10; i++ {
		nonce := "n-" + string(rune('a'+i))
		ok, _ := s.SeenOrStore(nonce)
		require.True(t, ok)
	}
	require.Equal(t, 10, s.Len())

	// Advance past TTL. The sweep cadence is far higher than the
	// number of calls so far — the map must still hold all entries.
	now = now.Add(time.Hour)
	ok, _ := s.SeenOrStore("post-ttl")
	require.True(t, ok)
	assert.Equal(t, 11, s.Len(),
		"sweep is deferred; stale entries persist until cadence is reached")
}

func TestMemoryNonceStore_WithSweepEvery_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() {
		NewMemoryNonceStore(time.Minute, WithSweepEvery(0))
	})
	assert.Panics(t, func() {
		NewMemoryNonceStore(time.Minute, WithSweepEvery(-5))
	})
}
