package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/approval"
	"github.com/bds421/rho-kit/data/approval/memory"
)

const (
	testKeyHeader = DefaultTenantHeader
	testTenantID  = "tenant-1"
	testActor     = "agent-1"
)

func newRequest(method, path string, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(testKeyHeader, testTenantID)
	r.Header.Set("X-Actor", testActor)
	return r
}

// headerActor is the canonical actor extractor used in tests. The
// kit refuses to default actors to "anonymous" on destructive
// operations, so every test that exercises the happy path wires this
// extractor explicitly.
func headerActor() Option {
	return WithActorExtractor(func(r *http.Request) (string, bool) {
		v := r.Header.Get("X-Actor")
		return v, v != ""
	})
}

func TestMiddleware_RecordsPendingAndReturns202(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor())

	// Downstream handler must NOT execute on the pending path. The
	// failing assertion runs in the test goroutine, not the handler,
	// so a sync flag is enough.
	var downstreamRan bool
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		downstreamRan = true
	}))

	rec := httptest.NewRecorder()
	body := `{"force":true,"reason":"GDPR"}`
	h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", body))

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.False(t, downstreamRan, "downstream must not run on pending creation")

	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.ApprovalID)
	assert.Equal(t, string(approval.StatePending), resp.Status)

	stored, err := store.Get(context.Background(), resp.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, testTenantID, stored.TenantID)
	assert.Equal(t, testActor, stored.Actor)
	assert.Equal(t, "DELETE /v1/users/42", stored.Action)
	assert.Equal(t, "/v1/users/42", stored.Resource)
	assert.JSONEq(t, body, string(stored.Payload))
}

func TestMiddleware_400WhenTenantMissing(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := httptest.NewRequest(http.MethodDelete, "/v1/users/42", nil)
	r.Header.Set("X-Actor", testActor)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMiddleware_401WhenActorMissing(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := httptest.NewRequest(http.MethodDelete, "/v1/users/42", nil)
	r.Header.Set(testKeyHeader, testTenantID)
	// Deliberately omit X-Actor.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddleware_PanicsWithoutActorExtractor(t *testing.T) {
	store := memory.New()
	assert.Panics(t, func() { Middleware(store) })
}

func TestMiddleware_413WhenBodyTooLarge(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor(), WithMaxBodyBytes(8))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "0123456789"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestMiddleware_BodyAtCapAccepted(t *testing.T) {
	// The "exactly at the cap" boundary case — exercising the off-by-
	// one we'd otherwise have between read-N+1 and len > N.
	store := memory.New()
	mw := Middleware(store, headerActor(), WithMaxBodyBytes(8))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "01234567"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestMiddleware_ActorExtraction(t *testing.T) {
	store := memory.New()
	mw := Middleware(store,
		WithActorExtractor(func(r *http.Request) (string, bool) {
			v := r.Header.Get("X-Actor")
			return v, v != ""
		}),
		WithActionExtractor(func(_ *http.Request) string { return "user.delete" }),
		WithResourceExtractor(func(_ *http.Request) string { return "users/42" }),
	)
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := newRequest(http.MethodDelete, "/v1/users/42", "")
	r.Header.Set("X-Actor", "agent-99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	stored, err := store.Get(context.Background(), resp.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, "agent-99", stored.Actor)
	assert.Equal(t, "user.delete", stored.Action)
	assert.Equal(t, "users/42", stored.Resource)
}

func TestMiddleware_TenantSourceOverride(t *testing.T) {
	// Services with tenant-on-context middleware in front need to
	// supply their own tenantSource. Verify the option does the right
	// thing.
	type ctxKey struct{}
	store := memory.New()
	mw := Middleware(store,
		headerActor(),
		WithTenantSource(func(r *http.Request) (string, bool) {
			v, ok := r.Context().Value(ctxKey{}).(string)
			return v, ok
		}))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	r.Header.Set("X-Actor", testActor)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, "ctx-tenant"))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	stored, err := store.Get(context.Background(), resp.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, "ctx-tenant", stored.TenantID)
}

func TestMiddleware_ExpiryDefault(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "{}"))

	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	stored, err := store.Get(context.Background(), resp.ApprovalID)
	require.NoError(t, err)

	assert.WithinDuration(t, time.Now().Add(DefaultExpiry), stored.ExpiresAt, 5*time.Second)
}

func TestMiddleware_PanicsOnNilStore(t *testing.T) {
	assert.Panics(t, func() { Middleware(nil, headerActor()) })
}

func TestWithMaxBodyBytes_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() { WithMaxBodyBytes(0) })
}

func TestWithExpiry_PanicsOnZero(t *testing.T) {
	assert.Panics(t, func() { WithExpiry(0) })
}

func TestWithTenantSource_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithTenantSource(nil) })
}

func TestWithActorExtractor_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithActorExtractor(nil) })
}

func TestWithActionExtractor_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithActionExtractor(nil) })
}

func TestWithResourceExtractor_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithResourceExtractor(nil) })
}

func TestWithIDFunc_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithIDFunc(nil) })
}

func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	store := memory.New()
	mw := Middleware(store, headerActor(), WithLogger(nil))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "{}"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestEnsureBodyBuffered_Replayable(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	r2 := EnsureBodyBuffered(r, []byte(`{"replayed":true}`))

	got := make([]byte, 17)
	_, err := r2.Body.Read(got)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(got, []byte("replayed")))
	assert.Equal(t, int64(17), r2.ContentLength)
}
