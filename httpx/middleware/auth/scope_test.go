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
// mimicking what RequireUserWithJWT sets after JWT verification.
func withScopes(r *http.Request, scopes string) *http.Request {
	return r.WithContext(scopesKey.Set(r.Context(), authScopes(scopes)))
}

func TestRequireScope_NoScopes_PassesThrough(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
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
