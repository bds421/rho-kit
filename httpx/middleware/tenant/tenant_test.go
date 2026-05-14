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
	mw := New(WithoutTenantRequired())
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

func TestNew_ExtractorPanicReturns500(t *testing.T) {
	called := false
	mw := New(WithExtractor(func(*http.Request) (coretenant.ID, error) {
		panic("extract failed")
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, called)
}

func TestNew_CustomExtractorMustReturnValidatedTenantID(t *testing.T) {
	called := false
	mw := New(WithExtractor(func(*http.Request) (coretenant.ID, error) {
		return coretenant.ID("a:b"), nil
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called)
}

func TestNew_DoesNotReflectInvalidTenantDetails(t *testing.T) {
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "secret-token/acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "tenant: invalid tenant ID")
	assert.NotContains(t, rec.Body.String(), "secret-token")
	assert.NotContains(t, rec.Body.String(), "forbidden byte")
	assert.NotContains(t, rec.Body.String(), "offset")
}

func TestNew_DoesNotReflectCustomExtractorInvalidDetails(t *testing.T) {
	mw := New(WithExtractor(func(*http.Request) (coretenant.ID, error) {
		return "", coretenant.ValidateID("secret-token/acme")
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "tenant: invalid tenant ID")
	assert.NotContains(t, rec.Body.String(), "secret-token")
	assert.NotContains(t, rec.Body.String(), "forbidden byte")
	assert.NotContains(t, rec.Body.String(), "offset")
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
		"comma combined":  "acme,evil",
		"invalid utf8":    string([]byte{0xff}),
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

func TestNew_RejectsDuplicateTenantHeader(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{name: "default required"},
		{name: "not required", opts: []Option{WithoutTenantRequired()}},
		{name: "allow missing on safe methods", opts: []Option{WithAllowMissingTenantOnSafeMethods()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			mw := New(tt.opts...)
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Add("X-Tenant-Id", "acme")
			req.Header.Add("X-Tenant-Id", "evil")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.False(t, called)
		})
	}
}

func TestHeaderExtractor_DuplicateErrorDoesNotReflectHeaderName(t *testing.T) {
	extract := HeaderExtractor("X-Secret-Token")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Add("X-Secret-Token", "acme")
	req.Header.Add("X-Secret-Token", "other")

	_, err := extract(req)
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestNew_RejectsBlankTenantHeaderEvenWhenNotRequired(t *testing.T) {
	called := false
	mw := New(WithoutTenantRequired())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called)
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
	mw := New(WithoutTenantRequired())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not run for invalid tenant ID")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "a:b")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"WithoutTenantRequired() only relaxes missing tenants — invalid IDs must still 400")
}

func TestHeaderExtractor_PanicsOnEmpty(t *testing.T) {
	assert.Panics(t, func() { HeaderExtractor("") })
}

func TestHeaderExtractor_PanicsOnInvalidHeaderName(t *testing.T) {
	assert.Panics(t, func() { HeaderExtractor("Bad Header") })
}

func TestNew_PanicsOnNilExtractor(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil extractor")
		}
	}()
	New(WithExtractor(nil))
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	New(nil)
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
	mw := New(WithAllowMissingTenantOnSafeMethods())
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
	mw := New(WithAllowMissingTenantOnSafeMethods())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNew_AllowMissingOnSafeMethods_PassesWithTenant(t *testing.T) {
	mw := New(WithAllowMissingTenantOnSafeMethods())
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
