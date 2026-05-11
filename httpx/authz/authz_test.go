package authz

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequirePermission_PanicsOnNilDeps(t *testing.T) {
	cases := []struct {
		name     string
		policy   Policy
		resource ResourceFunc
		subject  SubjectFunc
	}{
		{"nil policy", nil, StaticResource("r"), SubjectFromUntrustedHeader("X-User-ID")},
		{"nil resource", AllowAll(), nil, SubjectFromUntrustedHeader("X-User-ID")},
		{"nil subject", AllowAll(), StaticResource("r"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic, got none")
				}
			}()
			RequirePermission(tc.policy, "read", tc.resource, tc.subject)
		})
	}
}

func TestRequirePermission_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		RequirePermission(AllowAll(), "read", StaticResource("r"), SubjectFromUntrustedHeader("X-User-ID"), nil)
	})
}

func TestRequirePermission_PanicsOnEmptyAction(t *testing.T) {
	assert.Panics(t, func() {
		RequirePermission(AllowAll(), "", StaticResource("r"), SubjectFromUntrustedHeader("X-User-ID"))
	})
}

func TestRequirePermission_PanicsOnInvalidAction(t *testing.T) {
	assert.PanicsWithValue(t, "authz: RequirePermission requires a valid action", func() {
		RequirePermission(AllowAll(), "read secret-token", StaticResource("r"), SubjectFromUntrustedHeader("X-User-ID"))
	})
}

func TestRequirePermission_Allowed(t *testing.T) {
	mw := RequirePermission(
		AllowAll(), "read",
		StaticResource("users"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("X-User-ID", "user-1")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequirePermission_Denied(t *testing.T) {
	mw := RequirePermission(
		DenyAll(), "delete",
		StaticResource("users"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/users/1", nil)
	r.Header.Set("X-User-ID", "user-1")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestRequirePermission_DeniedLogRedactsSubjectAndResource(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	mw := RequirePermission(
		DenyAll(), "delete",
		StaticResource("tenant-secret-resource"),
		SubjectFromUntrustedHeader("X-User-ID"),
		WithLogger(logger),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/users/42", nil)
	r.Header.Set("X-User-ID", "tenant-secret-subject")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	out := buf.String()
	assert.Contains(t, out, `"subject"`)
	assert.Contains(t, out, `"resource"`)
	assert.NotContains(t, out, "tenant-secret-subject")
	assert.NotContains(t, out, "tenant-secret-resource")
}

func TestRequirePermission_PolicyError(t *testing.T) {
	errPolicy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		return false, errors.New("opa unreachable")
	})

	mw := RequirePermission(
		errPolicy, "read",
		StaticResource("users"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("X-User-ID", "user-1")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestRequirePermission_EmptySubject(t *testing.T) {
	mw := RequirePermission(
		AllowAll(), "read",
		StaticResource("users"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	// No X-User-ID header — empty subject.
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestRequirePermission_InvalidSubject(t *testing.T) {
	var policyCalled bool
	policy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		policyCalled = true
		return true, nil
	})
	mw := RequirePermission(
		policy, "read",
		StaticResource("users"),
		SubjectFromContext(func(context.Context) string { return "user 1" }),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, policyCalled)
}

func TestRequirePermission_DuplicateSubjectHeaderReturns401(t *testing.T) {
	var policyCalled, handlerCalled bool
	policy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		policyCalled = true
		return true, nil
	})
	mw := RequirePermission(
		policy, "read",
		StaticResource("users"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Add("X-User-ID", "alice")
	r.Header.Add("X-User-ID", "bob")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, policyCalled)
	assert.False(t, handlerCalled)
}

func TestRequirePermission_SubjectPanicReturns401(t *testing.T) {
	called := false
	mw := RequirePermission(
		AllowAll(), "read",
		StaticResource("users"),
		func(*http.Request) string { panic("subject failed") },
	)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, r)
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called)
}

func TestRequirePermission_ResourcePanicReturns500(t *testing.T) {
	called := false
	mw := RequirePermission(
		AllowAll(), "read",
		func(*http.Request) string { panic("resource failed") },
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("X-User-ID", "user-1")

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, r)
	})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, called)
}

func TestRequirePermission_InvalidResource(t *testing.T) {
	var policyCalled bool
	policy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		policyCalled = true
		return true, nil
	})
	mw := RequirePermission(
		policy, "read",
		func(*http.Request) string { return "bad resource" },
		SubjectFromUntrustedHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("X-User-ID", "user-1")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, policyCalled)
}

func TestRequirePermission_PolicyPanicReturns500(t *testing.T) {
	called := false
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	panicPolicy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		panic("tenant-secret-policy failed")
	})
	mw := RequirePermission(
		panicPolicy, "read",
		StaticResource("tenant-secret-resource"),
		SubjectFromUntrustedHeader("X-User-ID"),
		WithLogger(logger),
	)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("X-User-ID", "tenant-secret-subject")

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, r)
	})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, called)

	out := buf.String()
	assert.NotContains(t, out, "tenant-secret-policy")
	assert.NotContains(t, out, "tenant-secret-subject")
	assert.NotContains(t, out, "tenant-secret-resource")
}

func TestAllowOnly_MatchesExactTriple(t *testing.T) {
	policy := AllowOnly("admin", "delete", "users")

	allowed, err := policy.Allowed(context.Background(), "admin", "delete", "users")
	assert.NoError(t, err)
	assert.True(t, allowed)

	denied, err := policy.Allowed(context.Background(), "user", "delete", "users")
	assert.NoError(t, err)
	assert.False(t, denied)
}

func TestResourceFromPath(t *testing.T) {
	mux := http.NewServeMux()

	var captured string
	mw := RequirePermission(
		AllowAll(), "read",
		ResourceFromPath("id"),
		SubjectFromUntrustedHeader("X-User-ID"),
	)
	mux.Handle("GET /users/{id}", mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.PathValue("id")
		w.WriteHeader(http.StatusOK)
	})))

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/42", nil)
	r.Header.Set("X-User-ID", "user-1")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "42", captured)
}

func TestStaticResource_PanicsOnInvalidResource(t *testing.T) {
	assert.PanicsWithValue(t, "authz: StaticResource requires a valid resource", func() { StaticResource("bad secret-token") })
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("invalid CIDR %q: %v", s, err)
	}
	return n
}

func TestSubjectFromTrustedHeader_RejectsUntrustedRemote(t *testing.T) {
	loopback := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	fn := SubjectFromTrustedHeader("X-User-ID", loopback)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.10:54321" // public IP, not in trusted list
	r.Header.Set("X-User-ID", "alice")

	assert.Equal(t, "", fn(r), "header from untrusted remote must be ignored")
}

func TestSubjectFromTrustedHeader_AcceptsTrustedProxy(t *testing.T) {
	loopback := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	fn := SubjectFromTrustedHeader("X-User-ID", loopback)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-User-ID", "alice")

	assert.Equal(t, "alice", fn(r))
}

func TestSubjectFromTrustedHeader_DetachesTrustedProxies(t *testing.T) {
	loopback := mustCIDR(t, "127.0.0.0/8")
	trusted := []*net.IPNet{loopback}
	fn := SubjectFromTrustedHeader("X-User-ID", trusted)

	replacement := mustCIDR(t, "203.0.113.0/24")
	loopback.IP = replacement.IP
	loopback.Mask = replacement.Mask
	trusted[0] = mustCIDR(t, "198.51.100.0/24")

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-User-ID", "alice")

	assert.Equal(t, "alice", fn(r))
}

func TestSubjectFromTrustedHeader_RejectsDuplicateHeader(t *testing.T) {
	loopback := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	fn := SubjectFromTrustedHeader("X-User-ID", loopback)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Add("X-User-ID", "alice")
	r.Header.Add("X-User-ID", "bob")

	assert.Equal(t, "", fn(r))
}

func TestSubjectFromTrustedHeader_RejectsAmbiguousIdentityValues(t *testing.T) {
	loopback := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}
	fn := SubjectFromTrustedHeader("X-User-ID", loopback)

	tests := map[string]string{
		"edge whitespace":     " alice ",
		"internal whitespace": "alice bob",
		"comma combined":      "alice,bob",
		"control":             "alice\nbob",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = "127.0.0.1:5000"
			r.Header.Set("X-User-ID", value)

			assert.Equal(t, "", fn(r))
		})
	}
}

func TestSubjectFromTrustedHeader_EmptyTrustedListRejectsAll(t *testing.T) {
	fn := SubjectFromTrustedHeader("X-User-ID", nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-User-ID", "alice")
	assert.Equal(t, "", fn(r))
}

func TestSubjectFromContext(t *testing.T) {
	type keyType struct{}

	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), keyType{}, "user-123"))

	fn := SubjectFromContext(func(ctx context.Context) string {
		v, _ := ctx.Value(keyType{}).(string)
		return v
	})
	assert.Equal(t, "user-123", fn(r))
}

func TestExtractorHelpers_PanicOnInvalidInput(t *testing.T) {
	assert.Panics(t, func() { ResourceFromPath("") })
	assert.Panics(t, func() { SubjectFromUntrustedHeader("") })
	assert.Panics(t, func() { SubjectFromUntrustedHeader("Bad Header") })
	assert.Panics(t, func() { SubjectFromTrustedHeader("", nil) })
	assert.Panics(t, func() { SubjectFromTrustedHeader("Bad Header", nil) })
	assert.Panics(t, func() { SubjectFromContext(nil) })
	assert.Panics(t, func() { StaticResource("") })
}
