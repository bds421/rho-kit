package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// withScopes returns a request whose context carries the given scopes string,
// mimicking what JWT sets after JWT verification.
func withScopes(r *http.Request, scopes string) *http.Request {
	return r.WithContext(scopesKey.Set(r.Context(), authScopes(scopes)))
}

// TestRequireScope_NoScopes_NoMarker_Denied is the regression test for the
// scope fail-open bug. Pre-fix, an empty scopes string passed through
// silently — including for routes mounted without any auth middleware.
func TestRequireScope_NoScopes_NoMarker_Denied(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for no scopes without trusted-S2S marker, got %d", rec.Code)
	}
}

func TestRequireScope_MatchingScope(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "admin,read")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequireScope_NonMatchingScope(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "read,write")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireScopeStrict_NoScopes_Denied(t *testing.T) {
	handler := RequireScopeStrict("admin")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireScopeStrict_EmptyScopes_Denied(t *testing.T) {
	handler := RequireScopeStrict("admin")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireScopeStrict_MatchingScope(t *testing.T) {
	handler := RequireScopeStrict("admin")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "admin,read")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequireScopeStrict_NonMatchingScope(t *testing.T) {
	handler := RequireScopeStrict("admin")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "read,write")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestHasScope_SingleScope(t *testing.T) {
	if !hasScope("admin", "admin") {
		t.Error("expected hasScope(\"admin\", \"admin\") to be true")
	}

	if hasScope("admin", "read") {
		t.Error("expected hasScope(\"admin\", \"read\") to be false")
	}
}

func TestHasScope_TrailingComma(t *testing.T) {
	if !hasScope("admin,", "admin") {
		t.Error("expected hasScope(\"admin,\", \"admin\") to be true")
	}
}

func TestHasScope_EmptySegments(t *testing.T) {
	if !hasScope("admin,,read", "admin") {
		t.Error("expected hasScope(\"admin,,read\", \"admin\") to be true")
	}

	if !hasScope("admin,,read", "read") {
		t.Error("expected hasScope(\"admin,,read\", \"read\") to be true")
	}
}

func TestHasScope_WhitespaceOnly(t *testing.T) {
	if hasScope("  ,  ", "admin") {
		t.Error("expected hasScope(\"  ,  \", \"admin\") to be false")
	}

	if hasScope("  ,  ", "read") {
		t.Error("expected hasScope(\"  ,  \", \"read\") to be false")
	}
}

// TestHasScope_SpaceSeparated is the regression test for the HTTP/gRPC scope
// grammar mismatch. Identity.Scopes is documented as "OAuth2-style
// space-separated" (strategy.go), the gRPC interceptor parses scopes with
// strings.Fields, and signed JWTs carry the raw scopes claim verbatim. A
// multi-scope token like "read write" must therefore be accepted by the HTTP
// scope check just as it is by gRPC RequireScopeUnary.
func TestHasScope_SpaceSeparated(t *testing.T) {
	if !hasScope("read write", "read") {
		t.Error("expected hasScope(\"read write\", \"read\") to be true")
	}

	if !hasScope("read write", "write") {
		t.Error("expected hasScope(\"read write\", \"write\") to be true")
	}

	if hasScope("read write", "admin") {
		t.Error("expected hasScope(\"read write\", \"admin\") to be false")
	}
}

// TestHasScope_MixedSeparators covers tokens that combine commas and
// whitespace (e.g. tabs, multiple spaces, comma plus space) so the predicate
// stays robust regardless of the producer's exact serialization.
func TestHasScope_MixedSeparators(t *testing.T) {
	if !hasScope("read, write", "write") {
		t.Error("expected hasScope(\"read, write\", \"write\") to be true")
	}

	if !hasScope("read\twrite", "write") {
		t.Error("expected hasScope(\"read\\twrite\", \"write\") to be true")
	}

	if !hasScope("read   write", "read") {
		t.Error("expected hasScope(\"read   write\", \"read\") to be true")
	}
}

func TestRequireScope_SpaceSeparatedScopes(t *testing.T) {
	handler := RequireScope("write")(okHandler())

	req := withScopes(httptest.NewRequest(http.MethodGet, "/", nil), "read write")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for space-separated scope match, got %d", rec.Code)
	}
}

func TestRequireScope_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty scope, got none")
		}
	}()
	RequireScope("")
}

func TestRequireScopeStrict_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty scope, got none")
		}
	}()
	RequireScopeStrict("")
}
