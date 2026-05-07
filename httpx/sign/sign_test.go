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

	"github.com/bds421/rho-kit/httpx/middleware/signedrequest"
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSign_WithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

func TestSign_WithNonceFn_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithNonceFn(nil) })
}

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
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
