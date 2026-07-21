package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

func TestAppendOutgoingIdentity_StampsSubjectAndEntitlements(t *testing.T) {
	ctx := context.Background()
	ctx = stampIdentity(ctx, Identity{
		Subject:     testUUID,
		Permissions: []string{"orders:read", "orders:write"},
		Scopes:      "api:read api:write",
	})

	req := httptest.NewRequest(http.MethodGet, "https://svc-b.internal/v1/orders", nil)
	AppendOutgoingIdentity(ctx, req)

	if got := req.Header.Get("X-User-Id"); got != testUUID {
		t.Fatalf("X-User-Id = %q, want %q", got, testUUID)
	}
	if got := req.Header.Get(HeaderPermissions); got != "orders:read orders:write" {
		t.Fatalf("X-Permissions = %q, want orders:read orders:write", got)
	}
	if got := req.Header.Get(HeaderScopes); got != "api:read api:write" {
		t.Fatalf("X-Scopes = %q, want api:read api:write", got)
	}
}

func TestAppendOutgoingIdentity_DoesNotOverwriteExisting(t *testing.T) {
	ctx := stampIdentity(context.Background(), Identity{
		Subject:     testUUID,
		Permissions: []string{"a"},
		Scopes:      "s",
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", "caller-set")
	req.Header.Set(HeaderPermissions, "keep")
	req.Header.Set(HeaderScopes, "keep-scope")
	AppendOutgoingIdentity(ctx, req)

	if got := req.Header.Get("X-User-Id"); got != "caller-set" {
		t.Fatalf("X-User-Id overwritten: %q", got)
	}
	if got := req.Header.Get(HeaderPermissions); got != "keep" {
		t.Fatalf("X-Permissions overwritten: %q", got)
	}
	if got := req.Header.Get(HeaderScopes); got != "keep-scope" {
		t.Fatalf("X-Scopes overwritten: %q", got)
	}
}

func TestAppendOutgoingIdentity_NilRequestNoops(t *testing.T) {
	AppendOutgoingIdentity(context.Background(), nil) // must not panic
}

// TestRequireS2SAuth_MTLS_AdoptsEntitlementHeaders verifies the full cross-hop
// path: upstream stamps X-Permissions/X-Scopes, mTLS admission adopts them,
// RequirePermission enforces the user claim without WithTrustedS2SBypass.
func TestRequireS2SAuth_MTLS_AdoptsEntitlementHeaders(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(
		RequirePermission("orders:write")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if got := Permissions(r.Context()); len(got) != 2 || got[0] != "orders:read" || got[1] != "orders:write" {
				t.Errorf("Permissions = %v, want [orders:read orders:write]", got)
			}
			if got := Scopes(r.Context()); got != "api:write" {
				t.Errorf("Scopes = %q, want api:write", got)
			}
			if !IsTrustedS2S(r.Context()) {
				t.Error("expected trusted-S2S marker")
			}
			w.WriteHeader(http.StatusOK)
		})),
	)

	req := withMTLS(httptest.NewRequest(http.MethodPost, "/orders", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	req.Header.Set(HeaderPermissions, "orders:read orders:write")
	req.Header.Set(HeaderScopes, "api:write")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for trusted-S2S with matching perms, got %d", rec.Code)
	}
	if !called {
		t.Error("handler should run when propagated perms match")
	}
}

// TestRequireS2SAuth_MTLS_MissingEntitlementsDeniedByRequirePermission is the
// permission-laundering regression: trusted mTLS without X-Permissions must
// not pass RequirePermission.
func TestRequireS2SAuth_MTLS_MissingEntitlementsDeniedByRequirePermission(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(
		RequirePermission("orders:write")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})),
	)

	req := withMTLS(httptest.NewRequest(http.MethodPost, "/orders", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for trusted-S2S without perms, got %d", rec.Code)
	}
	if called {
		t.Error("handler must not run without matching permissions")
	}
}
