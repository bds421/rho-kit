package tenant

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/httpx/v2"
)

// headerOpt rebuilds the v1-style header-default middleware so tests
// that exercise X-Tenant-Id semantics keep a clear, explicit handle on
// that opt-in path.
func headerOpt() Option { return WithExtractor(HeaderExtractor("X-Tenant-Id")) }

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

func TestNew_DefaultContextExtractor_PassesWhenTenantOnCtx(t *testing.T) {
	mw := New()
	handler := mw(okHandler(t, coretenant.ID("acme")))

	id, err := coretenant.NewID("acme")
	require.NoError(t, err)
	ctx, err := coretenant.WithID(context.Background(), id)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_DefaultContextExtractor_IgnoresHeader(t *testing.T) {
	// Header trust is OFF by default — the kit must not read
	// X-Tenant-Id unless WithExtractor(HeaderExtractor(...)) is set.
	mw := New()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"default extractor reads ctx only — inbound X-Tenant-Id must not satisfy the requirement")
}

func TestNew_HeaderExtractor_OptIn(t *testing.T) {
	mw := New(headerOpt())
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_RejectsMissingOnPOST(t *testing.T) {
	mw := New(headerOpt())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNew_RejectsMissingOnGET_DefaultRequired(t *testing.T) {
	// Default behavior: the require-tenant rule applies to every
	// method, including GET/HEAD/OPTIONS. The previous safe-method
	// short-circuit was the source of a tenant-budget bypass.
	mw := New(headerOpt())
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

func TestContextExtractor_AbsentReturnsZeroNoError(t *testing.T) {
	extract := ContextExtractor()
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	id, err := extract(req)
	require.NoError(t, err)
	assert.True(t, id.IsZero(),
		"absence on ctx must map to the zero ID so the middleware's "+
			"require-tenant rule decides the response code")
}

func TestContextExtractor_ReturnsPreviouslyInjectedID(t *testing.T) {
	want, err := coretenant.NewID("acme")
	require.NoError(t, err)
	ctx, err := coretenant.WithID(context.Background(), want)
	require.NoError(t, err)

	extract := ContextExtractor()
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

	got, err := extract(req)
	require.NoError(t, err)
	assert.Equal(t, want, got)
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

func TestNew_ExtractorPanicLogsViaRequestScopedLogger(t *testing.T) {
	// Panic diagnostics must flow through the request-scoped logger the
	// stack middleware installs (httpx.SetLogger), not slog.Default(),
	// so they reach the service's configured handler/sink. A request
	// carrying its own logger must capture the panic; slog.Default()
	// must not see it.
	buf := &bytes.Buffer{}
	reqLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	defaultBuf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(defaultBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	mw := New(WithExtractor(func(*http.Request) (coretenant.ID, error) {
		panic("extract failed")
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ctx := httpx.SetLogger(context.Background(), reqLogger)
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "tenant: extractor panicked",
		"panic must be logged through the request-scoped logger")
	assert.Empty(t, defaultBuf.String(),
		"panic must not bypass the request-scoped logger to slog.Default()")
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
	mw := New(headerOpt())
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
	mw := New(headerOpt())
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
		{name: "default required", opts: []Option{headerOpt()}},
		{name: "not required", opts: []Option{headerOpt(), WithoutTenantRequired()}},
		{name: "allow missing on safe methods", opts: []Option{headerOpt(), WithAllowMissingTenantOnSafeMethods()}},
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
	mw := New(headerOpt(), WithoutTenantRequired())
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
	mw := New(headerOpt())
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
	mw := New(headerOpt(), WithoutTenantRequired())
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
	mw := New(headerOpt())
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
	mw := New(headerOpt(), WithAllowMissingTenantOnSafeMethods())
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
	mw := New(headerOpt(), WithAllowMissingTenantOnSafeMethods())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNew_AllowMissingOnSafeMethods_PassesWithTenant(t *testing.T) {
	mw := New(headerOpt(), WithAllowMissingTenantOnSafeMethods())
	handler := mw(okHandler(t, coretenant.ID("acme")))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
