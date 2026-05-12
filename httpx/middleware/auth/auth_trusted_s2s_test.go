//go:build authtest

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequirePermission_TrustedS2S_PassesThrough confirms that requests
// carrying the trusted-S2S marker bypass the permission check. The marker
// is set only by RequireS2SAuth's mTLS branch; the test sets it via the
// WithTrustedS2S helper (available under the authtest build tag) to
// exercise the same path.
func TestRequirePermission_TrustedS2S_PassesThrough(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithTrustedS2S(ctx)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for trusted-S2S caller, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called for trusted-S2S caller")
	}
}

// TestPermissionByMethod_TrustedS2S_PassesThrough mirrors
// TestRequirePermission_TrustedS2S_PassesThrough for the by-method variant.
func TestPermissionByMethod_TrustedS2S_PassesThrough(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithTrustedS2S(ctx)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for trusted-S2S caller, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called for trusted-S2S caller")
	}
}

// TestRequireScope_TrustedS2S_PassesThrough confirms the trusted-S2S
// marker bypasses the scope check, mirroring RequirePermission.
func TestRequireScope_TrustedS2S_PassesThrough(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithTrustedS2S(req.Context()))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for trusted-S2S caller, got %d", rec.Code)
	}
}
