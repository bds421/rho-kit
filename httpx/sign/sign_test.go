package sign

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
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

type mutableKeyStore struct {
	keyID  string
	secret []byte
}

func (s *mutableKeyStore) CurrentKeyID(context.Context) (string, []byte, error) {
	return s.keyID, append([]byte(nil), s.secret...), nil
}

// blockingKeyStore blocks CurrentKeyID until ctx is cancelled. Used
// to prove that WrapKeyStore does not perform synchronous startup
// I/O — a remote secret manager outage at boot must not pin
// construction forever (R2-004).
type blockingKeyStore struct{}

func (blockingKeyStore) CurrentKeyID(ctx context.Context) (string, []byte, error) {
	<-ctx.Done()
	return "", nil, ctx.Err()
}

func TestWrapKeyStore_DoesNotBlockOnRemoteProvider(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_ = WrapKeyStore(http.DefaultTransport, blockingKeyStore{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WrapKeyStore blocked on KeyStore.CurrentKeyID — startup I/O leaked into construction")
	}
}

func TestWrapKeyStoreContext_RespectsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := WrapKeyStoreContext(ctx, http.DefaultTransport, blockingKeyStore{})
	require.Error(t, err, "WrapKeyStoreContext must surface the deadline-exceeded error from the blocked KeyStore")
}

// Round-trip end-to-end: client signs, server verifies. The most
// useful guarantee a kit can give callers is "signer + verifier agree
// on the wire format" — a single test that runs both proves it.
func TestSignAndVerify_RoundTrip(t *testing.T) {
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(_ context.Context, id string) ([]byte, error) {
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

func TestWrapKeyStore_UsesCurrentKeyPerRequest(t *testing.T) {
	const (
		oldKeyID  = "prod-old"
		newKeyID  = "prod-new"
		oldSecret = "this-is-32-bytes-of-old-secret!!"
		newSecret = "this-is-32-bytes-of-new-secret!!"
	)
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(_ context.Context, id string) ([]byte, error) {
		switch id {
		case oldKeyID:
			return []byte(oldSecret), nil
		case newKeyID:
			return []byte(newSecret), nil
		default:
			return nil, errors.New("unknown key")
		}
	}
	mw := signedrequest.Middleware(resolver, store)

	var seen []string
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get(signedrequest.HeaderKeyID))
		w.WriteHeader(http.StatusNoContent)
	})))
	defer srv.Close()

	keys := &mutableKeyStore{keyID: oldKeyID, secret: []byte(oldSecret)}
	client := &http.Client{Transport: WrapKeyStore(http.DefaultTransport, keys)}

	resp, err := client.Post(srv.URL+"/api/x", "application/json", strings.NewReader("old"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	keys.keyID = newKeyID
	keys.secret = []byte(newSecret)

	resp, err = client.Post(srv.URL+"/api/x", "application/json", strings.NewReader("new"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assert.Equal(t, []string{oldKeyID, newKeyID}, seen)
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

func TestSign_RejectsUnsafeKeyID(t *testing.T) {
	for _, id := range []string{
		strings.Repeat("k", keyIDMaxLen+1),
		"key\nid",
		" key",
		"key ",
		"key,other",
	} {
		t.Run(id, func(t *testing.T) {
			assert.Panics(t, func() {
				Wrap(http.DefaultTransport, []byte(secret), id)
			})
		})
	}
	assert.PanicsWithValue(t, "sign: keyID is invalid", func() {
		Wrap(http.DefaultTransport, []byte(secret), strings.Repeat("k", keyIDMaxLen+1))
	})
}

func TestSign_WithBodyMaxSize_RejectsNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithBodyMaxSize(0) })
	assert.Panics(t, func() { WithBodyMaxSize(-1) })
}

func TestSign_WithIncludeHeadersClonesInput(t *testing.T) {
	names := []string{"X-Tenant-Id"}
	opt := WithIncludeHeaders(names...)
	names[0] = "X-Mutated"

	var cfg config
	opt(&cfg)

	require.Equal(t, []string{"x-tenant-id"}, cfg.includeHeaders)
}

func TestSign_BufferBodyErrorsAreStable(t *testing.T) {
	readErr := errors.New("reader failed for secret-token")
	readReq := httptest.NewRequest(http.MethodPost, "http://example.test/upload", nil)
	readReq.Body = readErrorBody{err: readErr}

	body, err := bufferBody(readReq, 1024)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "sign: read request body failed", err.Error())
	assert.ErrorIs(t, err, readErr)
	assert.NotContains(t, err.Error(), "secret-token")

	closeErr := errors.New("close failed for secret-token")
	closeReq := httptest.NewRequest(http.MethodPost, "http://example.test/upload", nil)
	closeReq.Body = closeErrorBody{Reader: bytes.NewReader([]byte("body")), err: closeErr}

	body, err = bufferBody(closeReq, 1024)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "sign: close request body failed", err.Error())
	assert.ErrorIs(t, err, closeErr)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSign_BufferBodySizeErrorIsStable(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.test/upload", bytes.NewReader([]byte("abcd")))

	body, err := bufferBody(req, 3)
	require.Error(t, err)
	assert.Nil(t, body)
	assert.Equal(t, "sign: body exceeds maximum size", err.Error())
	assert.NotContains(t, err.Error(), "3")
	assert.NotContains(t, err.Error(), "4")
}

func TestSign_NoBody(t *testing.T) {
	store := signedrequest.NewMemoryNonceStore(10 * time.Minute)
	resolver := func(context.Context, string) ([]byte, error) { return []byte(secret), nil }
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

func TestSign_WithIncludeHeadersPanicDoesNotReflectInvalidName(t *testing.T) {
	assert.PanicsWithValue(t,
		"sign: WithIncludeHeaders requires a valid HTTP header field name",
		func() { WithIncludeHeaders("Bad Header secret-token") },
	)
}

func TestSign_RoundTripReturnsNonceError(t *testing.T) {
	prev := nonceRandReader
	nonceRandReader = failingReader{}
	t.Cleanup(func() { nonceRandReader = prev })

	rt := Wrap(roundTripFn(func(*http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be called when nonce generation fails")
		return nil, nil
	}), []byte(secret), keyID)

	req := httptest.NewRequest(http.MethodGet, "https://example.com/resource", nil)
	resp, err := rt.RoundTrip(req)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "generate nonce")
}

func TestSign_WrapPanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		Wrap(http.DefaultTransport, []byte(secret), keyID, nil)
	})
}

func TestSign_WrapNilBaseUsesKitTransportWhenDefaultTransportReplaced(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripFn(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	rt := Wrap(nil, []byte(secret), keyID)
	wrapped, ok := rt.(*transport)
	require.True(t, ok)
	if _, ok := wrapped.base.(*http.Transport); !ok {
		t.Fatalf("nil base = %T, want *http.Transport fallback", wrapped.base)
	}
}

func TestSign_RoundTripInvalidRequestReturnsError(t *testing.T) {
	emptyMethod, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	require.NoError(t, err)
	emptyMethod.Method = ""
	invalidMethod, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	require.NoError(t, err)
	invalidMethod.Method = "GET\nsecret-token"
	invalidHost, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	require.NoError(t, err)
	invalidHost.Host = "secret-token bad"

	cases := []struct {
		name     string
		req      *http.Request
		notInErr string
	}{
		{name: "nil request", req: nil},
		{name: "nil URL", req: &http.Request{Method: http.MethodGet, Header: make(http.Header)}},
		{name: "empty method", req: emptyMethod},
		{name: "invalid method", req: invalidMethod, notInErr: "secret-token"},
		{name: "empty host", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: make(http.Header)}},
		{name: "invalid host", req: invalidHost, notInErr: "secret-token"},
		{
			name: "CRLF request URI",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Scheme: "https", Host: "example.test", Path: "/safe", RawQuery: "ok=1\r\nX-Evil: injected"},
				Header: make(http.Header),
			},
			notInErr: "X-Evil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			rt := Wrap(roundTripFn(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
			}), []byte(secret), keyID)

			resp, err := rt.RoundTrip(tc.req)
			assert.Nil(t, resp)
			assert.True(t, errors.Is(err, ErrInvalidRequest), "got %v", err)
			assert.False(t, called)
			if tc.notInErr != "" {
				assert.NotContains(t, err.Error(), tc.notInErr)
			}
		})
	}
}

func TestSign_RoundTripInitializesNilCloneHeader(t *testing.T) {
	var captured *http.Request
	intercept := roundTripFn(func(r *http.Request) (*http.Response, error) {
		captured = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})

	rt := Wrap(intercept, []byte(secret), keyID)
	req, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	require.NoError(t, err)
	req.Header = nil

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.NotNil(t, captured)
	assert.NotEmpty(t, captured.Header.Get(signedrequest.HeaderSignature))
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
	resolver := func(context.Context, string) ([]byte, error) { return []byte(secret), nil }
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

func TestSign_IncludeHeadersRejectsInvalidHeaderValue(t *testing.T) {
	rt := Wrap(roundTripFn(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	}), []byte(secret), keyID, WithIncludeHeaders("X-Tenant-ID"))

	req, err := http.NewRequest(http.MethodPost, "http://example.test/api/x", bytes.NewReader([]byte("body")))
	require.NoError(t, err)
	req.Header.Set("X-Tenant-ID", "acme\r\nX-Evil: 1")

	resp, err := rt.RoundTrip(req)
	assert.Nil(t, resp)
	assert.True(t, errors.Is(err, signedrequest.ErrInvalidRequest), "got %v", err)
}
