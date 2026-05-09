package sign

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

const (
	keyID  = "prod-1"
	secret = "this-is-32-bytes-of-test-secret!"
)

// Round-trip end-to-end: client signs, server verifies. The most
// useful guarantee a kit can give callers is "signer + verifier agree
// on the wire format" — a single test that runs both proves it.
func TestSignAndVerify_RoundTrip(t *testing.T) {
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(id string) ([]byte, error) {
		assert.Equal(t, keyID, id)
		return []byte(secret), nil
	}
	mw := signedrequest.Middleware(resolver, store)

	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})))
	defer srv.Close()

	client := &http.Client{Transport: Wrap(http.DefaultTransport, []byte(secret), keyID)}

	resp, err := client.Post(srv.URL+"/api/x", "application/json", bytes.NewReader([]byte(`{"hello":"world"}`)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	echoed, _ := io.ReadAll(resp.Body)
	assert.Equal(t, `{"hello":"world"}`, string(echoed))
}

func TestSign_RejectsEmptySecret(t *testing.T) {
	assert.Panics(t, func() {
		Wrap(http.DefaultTransport, nil, keyID)
	})
}

func TestSign_RejectsShortSecret(t *testing.T) {
	short := bytes.Repeat([]byte("a"), 31)
	assert.Panics(t, func() {
		Wrap(http.DefaultTransport, short, keyID)
	})
}

func TestSign_AcceptsExactly32ByteSecret(t *testing.T) {
	exact := bytes.Repeat([]byte("a"), 32)
	assert.NotPanics(t, func() {
		Wrap(http.DefaultTransport, exact, keyID)
	})
}

func TestSign_RejectsEmptyKeyID(t *testing.T) {
	assert.Panics(t, func() {
		Wrap(http.DefaultTransport, []byte(secret), "")
	})
}

func TestSign_WithBodyMaxSize_RejectsNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithBodyMaxSize(0) })
	assert.Panics(t, func() { WithBodyMaxSize(-1) })
}

func TestSign_NoBody(t *testing.T) {
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(string) ([]byte, error) { return []byte(secret), nil }
	mw := signedrequest.Middleware(resolver, store)
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })))
	defer srv.Close()

	client := &http.Client{Transport: Wrap(http.DefaultTransport, []byte(secret), keyID)}
	resp, err := client.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSign_WithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

func TestSign_WithNonceFn_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithNonceFn(nil) })
}

// FR-023 [HIGH] regression: the wrapper used to read the body via a
// shallow Clone, draining the caller's req.Body. Outer retry/auth
// wrappers that re-read the original request would see an empty body
// the second time around. After the fix, the caller's request body
// MUST still be readable post-RoundTrip.
func TestSign_PreservesCallerBodyAfterRoundTrip(t *testing.T) {
	// Stub base transport so we can avoid a real server and just
	// inspect the request the wrapper produced.
	base := http.NewServeMux()
	base.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(base)
	defer srv.Close()

	rt := Wrap(http.DefaultTransport, []byte(secret), keyID)

	payload := []byte(`{"original":"intact"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(payload))
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The caller's request body MUST still be readable. Pre-fix this
	// returned nil bytes because Clone shared the Body pointer and the
	// wrapper drained it.
	require.NotNil(t, req.Body, "req.Body should not be nil after RoundTrip")
	got, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "caller body drained — FR-023 regression")

	// ContentLength should be preserved so callers and wrappers can
	// trust the metadata.
	assert.Equal(t, int64(len(payload)), req.ContentLength)
}

// FR-023 follow-up: clone.GetBody should be set so net/http's
// redirect / 100-Continue / Auth-replay paths can replay the body
// without re-reading from a (possibly drained) Body reader.
func TestSign_CloneGetBodyEnablesReplay(t *testing.T) {
	// Capture the request the wrapper produced by intercepting the
	// base RoundTripper.
	var captured *http.Request
	intercept := roundTripFn(func(r *http.Request) (*http.Response, error) {
		captured = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	rt := Wrap(intercept, []byte(secret), keyID)
	payload := []byte("retryable-payload")
	req, err := http.NewRequest(http.MethodPost, "http://example/", bytes.NewReader(payload))
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.NotNil(t, captured)

	// First read of captured.Body — what the base transport would do.
	first, err := io.ReadAll(captured.Body)
	require.NoError(t, err)
	assert.Equal(t, payload, first)

	// Now exercise GetBody — net/http calls this on redirects.
	require.NotNil(t, captured.GetBody, "GetBody must be set so redirect/replay works")
	rc, err := captured.GetBody()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	second, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, payload, second)
}

// roundTripFn lets a test pose as an http.RoundTripper without
// spinning up an HTTP server.
type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSign_IncludeHeaders_BoundIntoSignature(t *testing.T) {
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(string) ([]byte, error) { return []byte(secret), nil }
	mw := signedrequest.Middleware(resolver, store, signedrequest.WithRequiredHeaders("X-Tenant-ID"))
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })))
	defer srv.Close()

	client := &http.Client{Transport: Wrap(http.DefaultTransport, []byte(secret), keyID, WithIncludeHeaders("X-Tenant-ID"))}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/x", bytes.NewReader([]byte("body")))
	require.NoError(t, err)
	req.Header.Set("X-Tenant-ID", "acme")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
