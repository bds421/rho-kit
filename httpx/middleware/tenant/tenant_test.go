package tenant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
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

func TestNew_RejectsMissingOnGET_DefaultRequired(t *testing.T) {
	// Default behavior: WithRequired(true) applies to every method,
	// including GET/HEAD/OPTIONS. The previous safe-method
	// short-circuit was the source of a tenant-budget bypass.
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
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
	mw := New(WithExtractor(func(r *http.Request) (coretenant.ID, error) {
		// Pretend we read it from a JWT claim.
		v := r.URL.Query().Get("tenant")
		if v == "" {
			return "", nil
		}
		return coretenant.NewID(v)
	}))
	handler := mw(okHandler(t, coretenant.ID("widgets")))

	req := httptest.NewRequest(http.MethodPost, "/?tenant=widgets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_RejectsInvalidTenantHeader(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	cases := map[string]string{
		"colon":           "a:b",
		"slash":           "a/b",
		"newline":         "a\nb",
		"tab":             "a\tb",
		"null":            "a\x00b",
		"too-long":        strings.Repeat("a", coretenant.MaxIDLen+1),
		"leading space":   " acme",
		"trailing space":  "acme ",
		"embedded space":  "ac me",
		"only whitespace": "   ",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("X-Tenant-Id", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestNew_RejectsInvalidEvenOnSafeMethod(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("X-Tenant-Id", "a:b")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"invalid tenant ID must be rejected even on safe methods — "+
					"it would otherwise reach handler context unvalidated")
		})
	}
}

func TestNew_RejectsInvalidEvenWithRequiredFalse(t *testing.T) {
	mw := New(WithRequired(false))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "a:b")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"WithRequired(false) only relaxes missing tenants — invalid IDs must still 400")
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

func TestNew_AllowMissingOnSafeMethods_PassesGET(t *testing.T) {
	// Explicit opt-out: GET/HEAD/OPTIONS without a tenant short-circuit
	// through the middleware while POST still requires one.
	mw := New(WithRequired(true), WithAllowMissingTenantOnSafeMethods())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code,
				"safe method without tenant must short-circuit when WithAllowMissingTenantOnSafeMethods is set")
		})
	}
}

func TestNew_AllowMissingOnSafeMethods_StillRejectsPOST(t *testing.T) {
	mw := New(WithRequired(true), WithAllowMissingTenantOnSafeMethods())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNew_AllowMissingOnSafeMethods_PassesWithTenant(t *testing.T) {
	mw := New(WithRequired(true), WithAllowMissingTenantOnSafeMethods())
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
