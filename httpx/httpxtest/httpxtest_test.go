package httpxtest_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2/httpxtest"
)

// echoLengthHandler reports, via response headers, what the server actually
// saw for the request body: the parsed Content-Length, whether the transfer
// was chunked, and the number of bytes successfully read from the body.
func echoLengthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		chunked := "false"
		for _, te := range r.TransferEncoding {
			if te == "chunked" {
				chunked = "true"
			}
		}
		w.Header().Set("X-Seen-Content-Length", strconv.FormatInt(r.ContentLength, 10))
		w.Header().Set("X-Seen-Chunked", chunked)
		w.Header().Set("X-Seen-Body-Len", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
	})
}

// TestDoRealServerRequestPreservesContentLength verifies that a pre-built
// request carrying a body (built via httptest.NewRequest, which wraps the
// body so net/http cannot auto-infer its length) is dispatched with the
// original Content-Length intact rather than silently switching to chunked
// transfer encoding.
func TestDoRealServerRequestPreservesContentLength(t *testing.T) {
	const payload = `{"hello":"world"}`

	// Build the request the way callers do. httptest.NewRequest sets
	// req.ContentLength from the *strings.Reader, but wraps the body in an
	// io.NopCloser whose concrete type net/http does NOT special-case for
	// ContentLength inference. The helper must therefore carry the explicit
	// length onto the rebuilt request, or the transfer silently switches to
	// chunked encoding (and req.GetBody is dropped).
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if req.ContentLength != int64(len(payload)) {
		// Sanity: confirm the source request carries an explicit length.
		t.Fatalf("precondition: source ContentLength = %d, want %d", req.ContentLength, len(payload))
	}

	resp := httpxtest.DoRealServerRequest(t, echoLengthHandler(), req)

	if got := resp.StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
	if got := resp.Header.Get("X-Seen-Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Errorf("server saw Content-Length %q, want %d (body switched to chunked or dropped)", got, len(payload))
	}
	if got := resp.Header.Get("X-Seen-Chunked"); got != "false" {
		t.Errorf("server saw chunked=%q, want false (Content-Length was lost)", got)
	}
	if got := resp.Header.Get("X-Seen-Body-Len"); got != strconv.Itoa(len(payload)) {
		t.Errorf("server read %q body bytes, want %d", got, len(payload))
	}
}

// TestDoRealServerRequestPreservesPathAndQuery verifies the documented
// contract that the path and query of the pre-built request are preserved
// when its URL is rewritten to target the test server.
func TestDoRealServerRequestPreservesPathAndQuery(t *testing.T) {
	var seenURI string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURI = r.URL.RequestURI()
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/items?page=2&sort=name", nil)
	resp := httpxtest.DoRealServerRequest(t, h, req)

	if got := resp.StatusCode; got != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", got, http.StatusNoContent)
	}
	if seenURI != "/items?page=2&sort=name" {
		t.Errorf("server saw URI %q, want %q", seenURI, "/items?page=2&sort=name")
	}
}

// TestDoRealServerRequestPreservesHostHeader verifies that a custom Host on
// the pre-built request is carried through to the server.
func TestDoRealServerRequestPreservesHostHeader(t *testing.T) {
	var seenHost string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "custom.example.com"
	resp := httpxtest.DoRealServerRequest(t, h, req)

	if got := resp.StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}
	if seenHost != "custom.example.com" {
		t.Errorf("server saw Host %q, want %q", seenHost, "custom.example.com")
	}
}
