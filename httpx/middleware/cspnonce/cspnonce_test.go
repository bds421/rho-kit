package cspnonce

import (
	"context"
	"encoding/json"
	"errors"
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

func TestMiddleware_NonceGenerationFailureUsesJSONError(t *testing.T) {
	old := nonceRandReader
	nonceRandReader = errReader{}
	t.Cleanup(func() { nonceRandReader = old })

	handler := Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run when nonce generation fails")
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "csp nonce generation failed", body.Error)
	assert.NotEmpty(t, body.Code)
}

func TestMiddleware_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		Middleware(nil)
	})
}

func TestWithBasePolicy_PanicsOnInvalidHeaderValue(t *testing.T) {
	assert.Panics(t, func() {
		WithBasePolicy("default-src 'self'\r\nX-Evil: injected")
	})
}

func TestWithBasePolicy_PanicsOnOuterWhitespace(t *testing.T) {
	assert.Panics(t, func() {
		WithBasePolicy(" default-src 'self'")
	})
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

func TestInjectNonce_RecognizesDirectiveWhitespace(t *testing.T) {
	got := injectNonce("default-src 'none'; script-src\t'self'; style-src\t\t'self'", "X")

	assert.Contains(t, got, "script-src\t'self' 'nonce-X'")
	assert.Contains(t, got, "style-src\t\t'self' 'nonce-X'")
	assert.Equal(t, 1, strings.Count(got, "script-src"))
	assert.Equal(t, 1, strings.Count(got, "style-src"))
}

func TestInjectNonce_EmptyPolicyProducesScriptAndStyle(t *testing.T) {
	got := injectNonce("", "X")
	assert.Contains(t, got, "script-src 'self' 'nonce-X'")
	assert.Contains(t, got, "style-src 'self' 'nonce-X'")
	assert.Equal(t, 1, strings.Count(got, "script-src"))
	assert.Equal(t, 1, strings.Count(got, "style-src"))
}

// A stricter base policy ("default-src 'none'") must not be silently
// widened: the auto-added script-src/style-src must inherit the
// default-src source list (here: none), so only the nonce is allowed —
// NOT 'self', which would re-enable same-origin script/style file loads
// the operator explicitly forbade.
func TestInjectNonce_DoesNotWidenDefaultSrcNone(t *testing.T) {
	got := injectNonce("default-src 'none'; object-src 'none'", "X")

	assert.Contains(t, got, "default-src 'none'")
	assert.Contains(t, got, "object-src 'none'")
	// Nonce-only: must NOT re-enable 'self'.
	assert.Contains(t, got, "script-src 'nonce-X'")
	assert.Contains(t, got, "style-src 'nonce-X'")
	assert.NotContains(t, got, "script-src 'self'")
	assert.NotContains(t, got, "style-src 'self'")
}

// When default-src carries a non-self source list, an auto-added
// script-src/style-src inherits that list so it enforces what the
// operator intended, plus the per-request nonce.
func TestInjectNonce_InheritsDefaultSrcSourceList(t *testing.T) {
	got := injectNonce("default-src 'self' https://cdn.example.com", "X")

	assert.Contains(t, got, "script-src 'self' https://cdn.example.com 'nonce-X'")
	assert.Contains(t, got, "style-src 'self' https://cdn.example.com 'nonce-X'")
}

// script-src-elem / style-src-elem take precedence over
// script-src / style-src for element-level enforcement, so the nonce
// must also be injected into them when the base policy declares them;
// otherwise nonced inline <script>/<style> elements stay blocked.
func TestInjectNonce_AugmentsElemDirectives(t *testing.T) {
	got := injectNonce("default-src 'self'; script-src-elem 'self'; style-src-elem 'self'", "X")

	assert.Contains(t, got, "script-src-elem 'self' 'nonce-X'")
	assert.Contains(t, got, "style-src-elem 'self' 'nonce-X'")
	// The plain script-src/style-src are still added (they govern other
	// fetch contexts and are harmless to keep nonced).
	assert.Contains(t, got, "script-src")
	assert.Contains(t, got, "style-src")
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable: secret-token")
}
