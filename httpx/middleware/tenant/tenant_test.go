package tenant

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/tenant"
)

func okHandler(t *testing.T, want coretenant.ID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := coretenant.FromContext(r.Context())
		if want != "" {
			assert.True(t, ok, "expected tenant in context")
			assert.Equal(t, want, got)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestNew_DefaultHeaderExtractor(t *testing.T) {
	mw := New()
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_RejectsMissingOnPOST(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNew_GETPassesWithoutTenant(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_NonRequiredPassesWithoutTenant(t *testing.T) {
	mw := New(WithRequired(false))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_CustomExtractor(t *testing.T) {
	mw := New(WithExtractor(func(r *http.Request) (coretenant.ID, bool) {
		// Pretend we read it from a JWT claim.
		if r.URL.Query().Get("tenant") == "" {
			return "", false
		}
		return coretenant.ID(r.URL.Query().Get("tenant")), true
	}))
	handler := mw(okHandler(t, coretenant.ID("widgets")))

	req := httptest.NewRequest(http.MethodPost, "/?tenant=widgets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHeaderExtractor_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty header")
		}
	}()
	HeaderExtractor("")
}

func TestNew_PanicsOnNilExtractor(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil extractor")
		}
	}()
	New(WithExtractor(nil))
}

func TestNew_EmptyHeaderTreatedAsMissing(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTenantMiddleware_SafeMethodEnforcement(t *testing.T) {
	// M-4 fix: WithRequiredOnSafeMethods(true) makes GET/HEAD/OPTIONS
	// reject when no tenant is supplied (instead of the default
	// short-circuit pass-through).
	mw := New(WithRequired(true), WithRequiredOnSafeMethods(true))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"safe method without tenant must reject when WithRequiredOnSafeMethods is true")
		})
	}
}

func TestTenantMiddleware_SafeMethodEnforcement_PassesWithTenant(t *testing.T) {
	mw := New(WithRequired(true), WithRequiredOnSafeMethods(true))
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTenantMiddleware_SafeMethodEnforcement_DefaultStillPasses(t *testing.T) {
	// Default (WithRequiredOnSafeMethods not set, i.e. false) preserves
	// the legacy short-circuit on safe methods so existing deployments
	// don't regress.
	mw := New(WithRequired(true))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
