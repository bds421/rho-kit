package cspnonce

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddleware_GeneratesNonceAndSetsHeader(t *testing.T) {
	var seenNonce string
	handler := Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenNonce = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.NotEmpty(t, seenNonce, "FromContext must return the per-request nonce")
	csp := rr.Header().Get("Content-Security-Policy")
	require.NotEmpty(t, csp)
	assert.Contains(t, csp, "'nonce-"+seenNonce+"'")
	assert.Contains(t, csp, "script-src")
	assert.Contains(t, csp, "style-src")
}

func TestMiddleware_NonceUniquePerRequest(t *testing.T) {
	var nonces []string
	handler := Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonces = append(nonces, FromContext(r.Context()))
	}))

	for range 5 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}

	seen := make(map[string]struct{})
	for _, n := range nonces {
		_, dup := seen[n]
		assert.False(t, dup, "nonce must be fresh per request")
		seen[n] = struct{}{}
	}
}

func TestMiddleware_AugmentsExistingScriptSrc(t *testing.T) {
	handler := Middleware(WithBasePolicy("default-src 'self'; script-src 'self' https://cdn.example.com"))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rr.Header().Get("Content-Security-Policy")

	assert.Contains(t, csp, "script-src 'self' https://cdn.example.com 'nonce-")
	// style-src wasn't in the base; the middleware adds it.
	assert.Contains(t, csp, "style-src 'self' 'nonce-")
}

func TestMiddleware_ReportOnlyHeaderName(t *testing.T) {
	handler := Middleware(WithReportOnly())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Empty(t, rr.Header().Get("Content-Security-Policy"))
	assert.NotEmpty(t, rr.Header().Get("Content-Security-Policy-Report-Only"))
}

func TestFromContext_AbsentMiddlewareReturnsEmpty(t *testing.T) {
	assert.Empty(t, FromContext(context.Background()))
}

func TestHTMLAttr_RendersNonceAttribute(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxKey{}, "abc123")
	assert.Equal(t, `nonce="abc123"`, string(HTMLAttr(ctx)))
}

func TestHTMLAttr_AbsentReturnsEmpty(t *testing.T) {
	assert.Empty(t, HTMLAttr(context.Background()))
}

func TestInjectNonce_PreservesOtherDirectives(t *testing.T) {
	got := injectNonce("default-src 'self'; img-src 'self' data:; connect-src 'self'", "X")
	// All three original directives must be present.
	assert.Contains(t, got, "default-src 'self'")
	assert.Contains(t, got, "img-src 'self' data:")
	assert.Contains(t, got, "connect-src 'self'")
	// Both nonce-bearing directives must be present.
	assert.Contains(t, got, "script-src 'self' 'nonce-X'")
	assert.Contains(t, got, "style-src 'self' 'nonce-X'")
}

func TestInjectNonce_EmptyPolicyProducesScriptAndStyle(t *testing.T) {
	got := injectNonce("", "X")
	assert.Contains(t, got, "script-src 'self' 'nonce-X'")
	assert.Contains(t, got, "style-src 'self' 'nonce-X'")
	assert.Equal(t, 1, strings.Count(got, "script-src"))
	assert.Equal(t, 1, strings.Count(got, "style-src"))
}
