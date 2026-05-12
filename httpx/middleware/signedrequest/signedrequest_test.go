package signedrequest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	sig, err := SignCanonical([]byte(secretStr), req, formatUnix(tsUnix), nonce, []byte(body), requiredHeaders)
	require.NoError(t, err)
	req.Header.Set(HeaderSignature, sig)
	// Re-attach the body so the middleware can read it. SignCanonical
	// did not consume; httptest.NewRequest already wraps the reader.
	if body != "" {
		req.Body = http.NoBody
		req.Body = newBody(body)
		req.ContentLength = int64(len(body))
	}
	return req
}

// makeNonce returns a deterministic 16-byte nonce, base64-encoded,
// derived from the seed string. Tests need real wire-format nonces
// because verify() now validates the nonce shape (audit FR-026).
func makeNonce(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return base64.StdEncoding.EncodeToString(h[:16])
}

func newBody(s string) *fakeReadCloser { return &fakeReadCloser{Reader: bytes.NewReader([]byte(s))} }

type fakeReadCloser struct {
	*bytes.Reader
	closed bool
}

func (f *fakeReadCloser) Close() error {
	f.closed = true
	return nil
}

type readErrorBody struct {
	err error
}

func (b readErrorBody) Read([]byte) (int, error) {
	return 0, b.err
}

func (b readErrorBody) Close() error {
	return nil
}

type closeErrorBody struct {
	*bytes.Reader
	err error
}

func (b closeErrorBody) Close() error {
	return b.err
}

func formatUnix(t int64) string { return strconv.FormatInt(t, 10) }

func TestVerify_RoundTrip(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)

	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := signRequest(t, "POST", "/api/x", "hello", time.Now(), makeNonce("nonce-1"), nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestVerify_ClosesOriginalBodyAfterRewind(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)

	var downstreamBody []byte
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		downstreamBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))

	req := signRequest(t, "POST", "/api/x", "hello", time.Now(), makeNonce("nonce-close"), nil, nil)
	originalBody := newBody("hello")
	req.Body = originalBody
	req.ContentLength = int64(originalBody.Len())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, originalBody.closed, "original request body should be closed after middleware rewinds it")
	assert.Equal(t, "hello", string(downstreamBody))
}

func TestReadBodyErrorsAreStable(t *testing.T) {
	readErr := errors.New("reader failed for secret-token")
	readReq := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	readReq.Body = readErrorBody{err: readErr}

	body, err := readBody(readReq, 1024)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "signedrequest: read body failed", err.Error())
	assert.ErrorIs(t, err, readErr)
	assert.NotContains(t, err.Error(), "secret-token")

	closeErr := errors.New("close failed for secret-token")
	closeReq := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	closeReq.Body = closeErrorBody{Reader: bytes.NewReader([]byte("body")), err: closeErr}

	body, err = readBody(closeReq, 1024)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "signedrequest: close body failed", err.Error())
	assert.ErrorIs(t, err, closeErr)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestVerify_RejectsReplayedNonce(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	now := time.Now()
	r1 := signRequest(t, "POST", "/x", "body", now, makeNonce("same-nonce"), nil, nil)
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, r1)
	require.Equal(t, http.StatusOK, rr1.Code)

	r2 := signRequest(t, "POST", "/x", "body", now, makeNonce("same-nonce"), nil, nil)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, r2)
	assert.Equal(t, http.StatusUnauthorized, rr2.Code)
	assertJSONError(t, rr2, "unauthorized")
}

func TestVerify_RejectsExpiredTimestamp(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithMaxClockSkew(time.Minute))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	old := time.Now().Add(-time.Hour)
	r := signRequest(t, "POST", "/x", "", old, makeNonce("nonce-old"), nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assertJSONError(t, rr, "bad request")
}

func TestVerify_RejectsExtremeTimestampWithoutOverflow(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	for name, tsUnix := range map[string]int64{
		"min": math.MinInt64,
		"max": math.MaxInt64,
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryNonceStore(10 * time.Minute)
			mw := Middleware(newResolver(t), store, WithClock(func() time.Time { return now }))
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

			body := []byte("body")
			ts := formatUnix(tsUnix)
			nonce := makeNonce("extreme-" + name)
			req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
			req.Header.Set(HeaderTimestamp, ts)
			req.Header.Set(HeaderNonce, nonce)
			req.Header.Set(HeaderKeyID, keyID)
			sig, err := SignCanonical([]byte(secretStr), req, ts, nonce, body, nil)
			require.NoError(t, err)
			req.Header.Set(HeaderSignature, sig)
			req.Body = newBody(string(body))
			req.ContentLength = int64(len(body))

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.Zero(t, store.Len(), "stale/future timestamp must be rejected before nonce storage")
		})
	}
}

func TestVerify_RejectsModifiedBody(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "original", time.Now(), makeNonce("nonce-mod"), nil, nil)
	// Tamper after signing.
	r.Body = newBody("tampered")
	r.ContentLength = int64(len("tampered"))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestVerify_RejectsWrongLengthSignature(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "body", time.Now(), makeNonce("short-signature"), nil, nil)
	r.Header.Set(HeaderSignature, signaturePrefix+base64.StdEncoding.EncodeToString([]byte("short")))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Zero(t, store.Len(), "malformed signature must not store the nonce")
}

func TestVerify_RejectsMethodCaseTampering(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "body", time.Now(), makeNonce("method-case"), nil, nil)
	r.Method = "post"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Zero(t, store.Len(), "tampered method must be rejected before nonce storage")
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

func TestRequiredHeaderLengthErrorIsStable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Test", "abcd")

	value, err := requiredSingletonHeaderBounded(req, "X-Test", 3)
	require.Error(t, err)
	assert.Empty(t, value)
	assert.ErrorIs(t, err, ErrInvalidRequest)
	assert.NotContains(t, err.Error(), "3")
	assert.NotContains(t, err.Error(), "4")
}

func TestVerify_RequiredHeaderEnforced(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithRequiredHeaders("X-Tenant-ID"))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	// With required header — round-trip succeeds.
	rOK := signRequest(t, "POST", "/x", "", time.Now(), makeNonce("n1"), []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
	rrOK := httptest.NewRecorder()
	h.ServeHTTP(rrOK, rOK)
	assert.Equal(t, http.StatusOK, rrOK.Code)

	// Same signing, but the header is dropped after signing → MAC mismatch.
	rBad := signRequest(t, "POST", "/x", "", time.Now(), makeNonce("n2"), []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
	rBad.Header.Del("X-Tenant-ID")
	rrBad := httptest.NewRecorder()
	h.ServeHTTP(rrBad, rBad)
	assert.Equal(t, http.StatusBadRequest, rrBad.Code)
}

func TestVerify_RejectsContentTypeTampering(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", `{"ok":true}`, time.Now(), makeNonce("content-type-tamper"), nil, map[string]string{"Content-Type": "application/json"})
	r.Header.Set("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestVerify_RejectsDuplicateContentTypeValues(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", `{"ok":true}`, time.Now(), makeNonce("content-type-dup"), nil, map[string]string{"Content-Type": "application/json"})
	r.Header.Add("Content-Type", "text/plain")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerify_RejectsInvalidCanonicalHeaderValues(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithRequiredHeaders("X-Tenant-ID"))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	t.Run("content type", func(t *testing.T) {
		r := signRequest(t, "POST", "/x", `{"ok":true}`, time.Now(), makeNonce("bad-content-type"), nil, map[string]string{"Content-Type": "application/json"})
		r.Header.Set("Content-Type", "application/json\r\nX-Evil: 1")

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("required header", func(t *testing.T) {
		r := signRequest(t, "POST", "/x", "", time.Now(), makeNonce("bad-required-header"), []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
		r.Header.Set("X-Tenant-ID", "acme\ncorp")

		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

func TestVerify_RejectsInvalidHost(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	body := []byte("body")
	ts := formatUnix(time.Now().UTC().Unix())
	nonce := makeNonce("invalid-host")
	r := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	r.Host = "bad host"
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderKeyID, keyID)
	r.Header.Set(HeaderSignature, signaturePrefix+base64.StdEncoding.EncodeToString(
		hmacSHA256([]byte(secretStr), buildCanonical(r, ts, nonce, body, nil)),
	))
	r.Body = newBody(string(body))
	r.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.False(t, called)
	assert.Zero(t, store.Len(), "invalid host must be rejected before nonce storage")
}

func TestVerify_RejectsDuplicateRequiredHeaderValues(t *testing.T) {
	store := NewMemoryNonceStore(10 * time.Minute)
	mw := Middleware(newResolver(t), store, WithRequiredHeaders("X-Tenant-ID"))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	r := signRequest(t, "POST", "/x", "", time.Now(), makeNonce("dup-required"), []string{"x-tenant-id"}, map[string]string{"X-Tenant-ID": "acme"})
	r.Header.Add("X-Tenant-ID", "other")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerify_RejectsDuplicateSignatureHeaders(t *testing.T) {
	headers := []string{HeaderTimestamp, HeaderNonce, HeaderKeyID, HeaderSignature}
	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			store := NewMemoryNonceStore(10 * time.Minute)
			mw := Middleware(newResolver(t), store)
			called := false
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			r := signRequest(t, "POST", "/x", "", time.Now(), makeNonce("dup-"+header), nil, nil)
			r.Header.Add(header, r.Header.Get(header))

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, called)
		})
	}
}

func TestVerify_RejectsOversizedSignatureHeadersBeforeResolver(t *testing.T) {
	headers := map[string]string{
		HeaderTimestamp: strings.Repeat("1", timestampMaxLen+1),
		HeaderNonce:     strings.Repeat("A", nonceMaxLen+1),
		HeaderKeyID:     strings.Repeat("k", keyIDMaxLen+1),
		HeaderSignature: signaturePrefix + strings.Repeat("A", signatureMaxLen-len(signaturePrefix)+1),
	}

	for header, value := range headers {
		t.Run(header, func(t *testing.T) {
			store := NewMemoryNonceStore(10 * time.Minute)
			resolverCalled := false
			mw := Middleware(func(string) ([]byte, error) {
				resolverCalled = true
				return []byte(secretStr), nil
			}, store)
			called := false
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			r := signRequest(t, "POST", "/x", "body", time.Now(), makeNonce("oversized-"+header), nil, nil)
			r.Header.Set(header, value)

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, resolverCalled, "oversized %s must not hit the key resolver", header)
			assert.False(t, called)
			assert.Zero(t, store.Len(), "oversized %s must not consume replay nonces", header)
		})
	}
}

func TestVerify_RejectsAmbiguousSignatureHeaderValuesBeforeResolver(t *testing.T) {
	now := time.Now()
	valid := signRequest(t, "POST", "/x", "body", now, makeNonce("ambiguous-base"), nil, nil)
	headers := map[string]string{
		HeaderTimestamp: " " + valid.Header.Get(HeaderTimestamp),
		HeaderNonce:     valid.Header.Get(HeaderNonce) + ",other",
		HeaderKeyID:     keyID + ",other",
		HeaderSignature: valid.Header.Get(HeaderSignature) + ",other",
	}

	for header, value := range headers {
		t.Run(header, func(t *testing.T) {
			store := NewMemoryNonceStore(10 * time.Minute)
			resolverCalled := false
			mw := Middleware(func(string) ([]byte, error) {
				resolverCalled = true
				return []byte(secretStr), nil
			}, store)
			called := false
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			r := signRequest(t, "POST", "/x", "body", now, makeNonce("ambiguous-"+header), nil, nil)
			r.Header.Set(header, value)

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)

			assert.Equal(t, http.StatusBadRequest, rr.Code)
			assert.False(t, resolverCalled, "ambiguous %s must not hit the key resolver", header)
			assert.False(t, called)
			assert.Zero(t, store.Len(), "ambiguous %s must not consume replay nonces", header)
		})
	}
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

	r := signRequest(t, "POST", "/x", "body", time.Now(), makeNonce("nonce-short"), nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assertJSONError(t, rr, "internal error")
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
	req.Header.Set(HeaderNonce, makeNonce("n-32-exact"))
	req.Header.Set(HeaderKeyID, keyID)
	sig, err := SignCanonical(exact, req, formatUnix(tsUnix), makeNonce("n-32-exact"), []byte(body), nil)
	require.NoError(t, err)
	req.Header.Set(HeaderSignature, sig)
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

func TestVerify_RejectsOversizedBodyAfterResolver(t *testing.T) {
	// The fix moves secret resolution before body buffering so an
	// unauthenticated caller cannot force the server to hold up to
	// bodyMaxSize bytes in memory per request. Body bytes are now
	// streamed through a SHA-256 hasher (no full buffer until MAC
	// passes), so the previous "resolver must not be called on
	// oversize" invariant becomes "resolver is called early, body
	// size still rejects, nonce store untouched on oversize".
	store := NewMemoryNonceStore(10 * time.Minute)
	resolverCalled := false
	resolver := func(string) ([]byte, error) {
		resolverCalled = true
		return []byte(secretStr), nil
	}
	mw := Middleware(resolver, store, WithBodyMaxSize(4))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	body := "too-large"
	req := signRequest(t, "POST", "/x", body, time.Now(), makeNonce("oversized"), nil, nil)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	assertJSONError(t, rr, "request entity too large")
	assert.True(t, resolverCalled, "resolver runs before body buffering to bound memory amplification")
	assert.Zero(t, store.Len(), "oversized bodies must not consume replay nonces")
}

func TestWithRequiredHeaders_PanicsOnEmptyName(t *testing.T) {
	assert.Panics(t, func() { WithRequiredHeaders("") })
}

func TestWithRequiredHeaders_ClonesInput(t *testing.T) {
	names := []string{"X-Tenant-ID"}
	opt := WithRequiredHeaders(names...)
	names[0] = "X-Mutated"

	var cfg config
	opt(&cfg)

	require.Equal(t, []string{"x-tenant-id"}, cfg.requiredHeaders)
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, want, body.Error)
	assert.NotEmpty(t, body.Code)
}

func TestWithRequiredHeaders_PanicsOnInvalidName(t *testing.T) {
	// Space is not a valid HTTP header field name character (RFC 7230).
	assert.Panics(t, func() { WithRequiredHeaders("Bad Header") })
	// Colon is also disallowed.
	assert.Panics(t, func() { WithRequiredHeaders("Bad:Header") })
}

func TestWithRequiredHeaders_PanicDoesNotReflectInvalidName(t *testing.T) {
	assert.PanicsWithValue(t,
		"signedrequest: WithRequiredHeaders requires a valid HTTP header field name",
		func() { WithRequiredHeaders("Bad Header secret-token") },
	)
}

func TestWithRequiredHeaders_PanicsWhenAnyNameInvalid(t *testing.T) {
	// First name is valid; second is empty. Whole call must panic.
	assert.Panics(t, func() { WithRequiredHeaders("X-Tenant-ID", "") })
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

func TestSignCanonical_RejectsInvalidInputs(t *testing.T) {
	validReq := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	validTS := formatUnix(time.Now().UTC().Unix())
	validNonce := makeNonce("canonical-valid")
	validSecret := []byte(secretStr)

	emptyMethod := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	emptyMethod.Method = ""
	invalidMethod := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	invalidMethod.Method = "POST\nsecret-token"
	emptyHost := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	emptyHost.Host = ""
	invalidHost := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	invalidHost.Host = "secret-token bad"

	cases := []struct {
		name     string
		secret   []byte
		req      *http.Request
		ts       string
		nonce    string
		headers  []string
		want     error
		notInErr string
	}{
		{name: "short secret", secret: []byte("short"), req: validReq, ts: validTS, nonce: validNonce, want: ErrSecretTooShort},
		{name: "nil request", secret: validSecret, req: nil, ts: validTS, nonce: validNonce, want: ErrInvalidRequest},
		{name: "nil URL", secret: validSecret, req: &http.Request{Method: http.MethodPost}, ts: validTS, nonce: validNonce, want: ErrInvalidRequest},
		{name: "empty method", secret: validSecret, req: emptyMethod, ts: validTS, nonce: validNonce, want: ErrInvalidRequest},
		{name: "invalid method", secret: validSecret, req: invalidMethod, ts: validTS, nonce: validNonce, want: ErrInvalidRequest, notInErr: "secret-token"},
		{name: "empty host", secret: validSecret, req: emptyHost, ts: validTS, nonce: validNonce, want: ErrInvalidRequest},
		{name: "invalid host", secret: validSecret, req: invalidHost, ts: validTS, nonce: validNonce, want: ErrInvalidRequest, notInErr: "secret-token"},
		{name: "missing timestamp", secret: validSecret, req: validReq, ts: "", nonce: validNonce, want: ErrMissingHeaders},
		{name: "invalid nonce", secret: validSecret, req: validReq, ts: validTS, nonce: "not-base64", want: ErrNonceInvalid},
		{name: "invalid required header", secret: validSecret, req: validReq, ts: validTS, nonce: validNonce, headers: []string{"Bad Header secret-token"}, want: ErrInvalidRequest, notInErr: "secret-token"},
		{name: "missing required header value", secret: validSecret, req: validReq, ts: validTS, nonce: validNonce, headers: []string{"X-Secret-Token"}, want: ErrMissingHeaders, notInErr: "secret-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, err := SignCanonical(tc.secret, tc.req, tc.ts, tc.nonce, nil, tc.headers)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v, want %v", err, tc.want)
			if tc.notInErr != "" {
				assert.NotContains(t, strings.ToLower(err.Error()), tc.notInErr)
			}
			assert.Empty(t, sig)
		})
	}
}

func TestSignCanonical_UsesURLHostWhenRequestHostEmpty(t *testing.T) {
	reqURLHost, err := http.NewRequest(http.MethodPost, "http://Example.COM/api/x", nil)
	require.NoError(t, err)
	reqURLHost.Host = ""
	reqHost, err := http.NewRequest(http.MethodPost, "http://Example.COM/api/x", nil)
	require.NoError(t, err)
	reqHost.Host = "example.com"

	ts := formatUnix(time.Now().UTC().Unix())
	nonce := makeNonce("url-host-fallback")
	body := []byte("payload")
	sigURLHost, err := SignCanonical([]byte(secretStr), reqURLHost, ts, nonce, body, nil)
	require.NoError(t, err)
	sigHost, err := SignCanonical([]byte(secretStr), reqHost, ts, nonce, body, nil)
	require.NoError(t, err)

	assert.Equal(t, sigHost, sigURLHost)
}

func TestSignCanonical_RejectsDuplicateRequiredHeaderValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Add("X-Secret-Token", "acme")
	req.Header.Add("X-Secret-Token", "other")

	sig, err := SignCanonical([]byte(secretStr), req, formatUnix(time.Now().UTC().Unix()), makeNonce("dup-sign"), nil, []string{"X-Secret-Token"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRequest), "got %v", err)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
	assert.Empty(t, sig)
}

func TestSignCanonical_RejectsInvalidCanonicalHeaderValues(t *testing.T) {
	ts := formatUnix(time.Now().UTC().Unix())
	nonce := makeNonce("bad-canonical-header")

	t.Run("content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
		req.Header.Set("Content-Type", "application/json\r\nX-Evil: 1")

		sig, err := SignCanonical([]byte(secretStr), req, ts, nonce, nil, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidRequest), "got %v", err)
		assert.Empty(t, sig)
	})

	t.Run("required header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
		req.Header.Set("X-Secret-Token", "acme\ncorp")

		sig, err := SignCanonical([]byte(secretStr), req, ts, nonce, nil, []string{"X-Secret-Token"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidRequest), "got %v", err)
		assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
		assert.Empty(t, sig)
	})
}

func TestMiddleware_PanicsOnNilOption(t *testing.T) {
	store := NewMemoryNonceStore(time.Minute)
	resolver := func(string) ([]byte, error) { return []byte("01234567890123456789012345678901"), nil }
	assert.Panics(t, func() {
		Middleware(resolver, store, nil)
	})
}

func TestMemoryNonceStore_Sweep(t *testing.T) {
	now := time.Now()
	s := NewMemoryNonceStore(time.Second)
	s.now = func() time.Time { return now }

	first, _ := s.SeenOrStore(context.Background(), "a")
	require.True(t, first)

	// Same instant → replay.
	second, _ := s.SeenOrStore(context.Background(), "a")
	require.False(t, second)

	// Past TTL → first-time again.
	now = now.Add(2 * time.Second)
	third, _ := s.SeenOrStore(context.Background(), "a")
	assert.True(t, third)
}

func TestMemoryNonceStore_InvalidReceiverReturnsError(t *testing.T) {
	for name, store := range map[string]*MemoryNonceStore{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := store.SeenOrStore(context.Background(), "nonce")
			assert.False(t, ok)
			assert.ErrorIs(t, err, ErrInvalidNonceStore)
			assert.Zero(t, store.Len())
		})
	}
}

func TestMemoryNonceStore_RejectsUnsafeNonce(t *testing.T) {
	store := NewMemoryNonceStore(time.Minute)
	for _, nonce := range []string{
		"",
		strings.Repeat("a", nonceMaxLen+1),
		"nonce\nvalue",
		"nonce value",
		"nonce\tvalue",
		string([]byte{'n', 'o', 0xff}),
	} {
		t.Run("invalid", func(t *testing.T) {
			ok, err := store.SeenOrStore(context.Background(), nonce)
			assert.False(t, ok)
			assert.ErrorIs(t, err, ErrNonceInvalid)
		})
	}
	assert.Zero(t, store.Len())
}

func TestMemoryNonceStore_WithSweepEvery_Immediate(t *testing.T) {
	// sweepEvery=1 means every call triggers a sweep, so an entry
	// older than TTL is reclaimed on the very next SeenOrStore.
	now := time.Now()
	s := NewMemoryNonceStore(time.Second, WithSweepEvery(1))
	s.now = func() time.Time { return now }

	first, _ := s.SeenOrStore(context.Background(), "a")
	require.True(t, first)
	require.Equal(t, 1, s.Len())

	// Advance past TTL and probe a *different* nonce. With sweepEvery=1
	// the sweep runs on this call and "a" is reclaimed before the
	// probe of "b" inserts.
	now = now.Add(2 * time.Second)
	first, _ = s.SeenOrStore(context.Background(), "b")
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
		ok, _ := s.SeenOrStore(context.Background(), nonce)
		require.True(t, ok)
	}
	require.Equal(t, 10, s.Len())

	// Advance past TTL. The sweep cadence is far higher than the
	// number of calls so far — the map must still hold all entries.
	now = now.Add(time.Hour)
	ok, _ := s.SeenOrStore(context.Background(), "post-ttl")
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

func TestMemoryNonceStore_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewMemoryNonceStore(time.Minute, nil)
	})
}
