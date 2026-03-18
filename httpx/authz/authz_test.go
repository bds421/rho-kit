package authz

import (
	"context"
	"errors"
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

func TestRequirePermission_Allowed(t *testing.T) {
	mw := RequirePermission(
		AllowAll(), "read",
		StaticResource("users"),
		SubjectFromHeader("X-User-ID"),
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
		SubjectFromHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/users/1", nil)
	r.Header.Set("X-User-ID", "user-1")
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestRequirePermission_PolicyError(t *testing.T) {
	errPolicy := policyFunc(func(context.Context, string, string, string) (bool, error) {
		return false, errors.New("opa unreachable")
	})

	mw := RequirePermission(
		errPolicy, "read",
		StaticResource("users"),
		SubjectFromHeader("X-User-ID"),
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
		SubjectFromHeader("X-User-ID"),
	)

	handler := mw(okHandler())
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users", nil)
	// No X-User-ID header — empty subject.
	handler.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
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
		SubjectFromHeader("X-User-ID"),
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
