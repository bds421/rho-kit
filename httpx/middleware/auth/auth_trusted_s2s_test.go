//go:build authtest

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequirePermission_TrustedS2S_DefaultDeniesWithoutPerms pins the safer
// default: the trusted-S2S marker alone does NOT satisfy RequirePermission
// when no permissions claim is present. Opt in with WithTrustedS2SBypass
// for service-level trust. Aligns with grpcx RequirePermissionUnary.
func TestRequirePermission_TrustedS2S_DefaultDeniesWithoutPerms(t *testing.T) {
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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for trusted-S2S without perms, got %d", rec.Code)
	}
	if called {
		t.Error("default must not launder permissions via trusted-S2S alone")
	}
}

// TestRequirePermission_TrustedS2S_BypassOptIn verifies that
// WithTrustedS2SBypass restores the historical short-circuit.
func TestRequirePermission_TrustedS2S_BypassOptIn(t *testing.T) {
	called := false
	h := RequirePermission("general:view", WithTrustedS2SBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		t.Fatalf("expected 200 with WithTrustedS2SBypass, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called with bypass opt-in")
	}
}

// TestPermissionByMethod_TrustedS2S_DefaultDeniesWithoutPerms mirrors
// RequirePermission default for the by-method variant.
func TestPermissionByMethod_TrustedS2S_DefaultDeniesWithoutPerms(t *testing.T) {
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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for trusted-S2S without perms, got %d", rec.Code)
	}
	if called {
		t.Error("default must not launder permissions via trusted-S2S alone")
	}
}

// TestPermissionByMethod_TrustedS2S_BypassOptIn mirrors RequirePermission
// bypass opt-in for the by-method variant.
func TestPermissionByMethod_TrustedS2S_BypassOptIn(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage", WithTrustedS2SBypass())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		t.Fatalf("expected 200 with WithTrustedS2SBypass, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called with bypass opt-in")
	}
}

// TestRequireScope_TrustedS2S_DefaultDeniesWithoutScopes pins fail-closed
// scope checks for trusted-S2S without claims.
func TestRequireScope_TrustedS2S_DefaultDeniesWithoutScopes(t *testing.T) {
	handler := RequireScope("admin")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithTrustedS2S(req.Context()))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for trusted-S2S without scopes, got %d", rec.Code)
	}
}

// TestRequireScope_TrustedS2S_BypassOptIn restores the historical short-circuit.
func TestRequireScope_TrustedS2S_BypassOptIn(t *testing.T) {
	handler := RequireScope("admin", WithTrustedS2SBypass())(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithTrustedS2S(req.Context()))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with WithTrustedS2SBypass, got %d", rec.Code)
	}
}

// TestRequirePermission_TrustedS2S_WithPropagatedPermsPasses verifies that
// trusted-S2S with adopted permissions still enforces the claim (allows when
// present).
func TestRequirePermission_TrustedS2S_WithPropagatedPermsPasses(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithTrustedS2S(ctx)
	ctx = WithPermissions(ctx, []string{"general:view"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when trusted-S2S carries matching perms, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called when perms match")
	}
}
