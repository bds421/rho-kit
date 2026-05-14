package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/data/v2/approval/memory"
)

const (
	testKeyHeader = DefaultTenantHeader
	testTenantID  = "tenant-1"
	testActor     = "agent-1"
)

// newRequest builds an httptest.NewRequest with a tenant already
// resolved into the request context (the v2 default trust path) and a
// canonical X-Actor header. Tests that exercise the legacy header
// trust path opt in explicitly via [WithTenantFromHeader].
func newRequest(method, path string, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx, err := coretenant.WithID(r.Context(), coretenant.MustNewID(testTenantID))
	if err != nil {
		panic(err)
	}
	r = r.WithContext(ctx)
	r.Header.Set("X-Actor", testActor)
	return r
}

// headerActor is the canonical actor extractor used in tests. The
// kit refuses to default actors to "anonymous" on destructive
// operations, so every test that exercises the happy path wires this
// extractor explicitly.
func headerActor() Option {
	return WithActorFromHeader("X-Actor")
}

func newApprovalStore(t *testing.T) *memory.Store {
	t.Helper()
	signer, err := approval.NewCursorSigner([]byte("test-approval-cursor-key-32-bytes"))
	require.NoError(t, err)
	return memory.New(signer)
}

func requireApprovalCount(t *testing.T, store *memory.Store, want int) {
	t.Helper()
	got, _, err := store.List(context.Background(), approval.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Len(t, got, want)
}

func TestMiddleware_RecordsPendingAndReturns202(t *testing.T) {
	store := newApprovalStore(t)
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

func TestMiddleware_DefaultMetadataUsesEscapedPath(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/files/a%2Fb", ""))

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	stored, err := store.Get(context.Background(), resp.ApprovalID)
	require.NoError(t, err)
	assert.Equal(t, "DELETE /v1/files/a%2Fb", stored.Action)
	assert.Equal(t, "/v1/files/a%2Fb", stored.Resource)
}

func TestMiddleware_400WhenTenantMissing(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := httptest.NewRequest(http.MethodDelete, "/v1/users/42", nil)
	r.Header.Set("X-Actor", testActor)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMiddleware_400WhenTenantHeaderAmbiguousOrInvalid(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*http.Request)
	}{
		{
			name: "duplicate",
			setup: func(r *http.Request) {
				r.Header.Del(testKeyHeader)
				r.Header.Add(testKeyHeader, "tenant-1")
				r.Header.Add(testKeyHeader, "tenant-2")
			},
		},
		{
			name: "blank",
			setup: func(r *http.Request) {
				r.Header.Set(testKeyHeader, "")
			},
		},
		{
			name: "invalid",
			setup: func(r *http.Request) {
				r.Header.Set(testKeyHeader, "tenant/1")
			},
		},
		{
			name: "comma combined",
			setup: func(r *http.Request) {
				r.Header.Set(testKeyHeader, "tenant-1,tenant-2")
			},
		},
		{
			name: "internal whitespace",
			setup: func(r *http.Request) {
				r.Header.Set(testKeyHeader, "tenant 1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newApprovalStore(t)
			// Header-trust path must be opted into explicitly in v2;
			// this test exercises that path's validation.
			mw := Middleware(store, headerActor(), WithTenantFromHeader(testKeyHeader))
			h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler should not run when tenant header is invalid")
			}))

			r := httptest.NewRequest(http.MethodDelete, "/v1/users/42", strings.NewReader(""))
			r.Header.Set(testKeyHeader, testTenantID)
			r.Header.Set("X-Actor", testActor)
			tt.setup(r)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			requireApprovalCount(t, store, 0)
		})
	}
}

func TestMiddleware_400WhenTenantSourcePanics(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store,
		headerActor(),
		WithTenantSource(func(*http.Request) (string, bool) {
			panic("tenant failed")
		}),
	)
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", ""))
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	requireApprovalCount(t, store, 0)
}

func TestMiddleware_401WhenActorMissing(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor())
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	r := httptest.NewRequest(http.MethodDelete, "/v1/users/42", nil)
	ctx, err := coretenant.WithID(r.Context(), coretenant.MustNewID(testTenantID))
	require.NoError(t, err)
	r = r.WithContext(ctx)
	// Deliberately omit X-Actor.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMiddleware_401WhenActorHeaderAmbiguousOrInvalid(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*http.Request)
	}{
		{
			name: "duplicate",
			setup: func(r *http.Request) {
				r.Header.Del("X-Actor")
				r.Header.Add("X-Actor", "agent-1")
				r.Header.Add("X-Actor", "agent-2")
			},
		},
		{
			name: "blank",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor", "")
			},
		},
		{
			name: "edge whitespace",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor", " agent-1 ")
			},
		},
		{
			name: "internal whitespace",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor", "agent 1")
			},
		},
		{
			name: "comma combined",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor", "agent-1,agent-2")
			},
		},
		{
			name: "control",
			setup: func(r *http.Request) {
				r.Header.Set("X-Actor", "agent\n1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newApprovalStore(t)
			mw := Middleware(store, headerActor())
			h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler should not run when actor header is invalid")
			}))

			r := newRequest(http.MethodDelete, "/v1/users/42", "")
			tt.setup(r)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			requireApprovalCount(t, store, 0)
		})
	}
}

func TestMiddleware_401WhenActorExtractorPanics(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, WithActorExtractor(func(*http.Request) (string, bool) {
		panic("actor failed")
	}))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", ""))
	})

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	requireApprovalCount(t, store, 0)
}

func TestMiddleware_PanicsWithoutActorExtractor(t *testing.T) {
	store := newApprovalStore(t)
	assert.Panics(t, func() { Middleware(store) })
}

func TestMiddleware_500WhenApprovalMetadataCallbackPanics(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{
			name: "id",
			opt: WithIDFunc(func() string {
				panic("id failed")
			}),
		},
		{
			name: "action",
			opt: WithActionExtractor(func(*http.Request) string {
				panic("action failed")
			}),
		},
		{
			name: "resource",
			opt: WithResourceExtractor(func(*http.Request) string {
				panic("resource failed")
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newApprovalStore(t)
			mw := Middleware(store, headerActor(), tt.opt)
			h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

			rec := httptest.NewRecorder()
			assert.NotPanics(t, func() {
				h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", ""))
			})

			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			requireApprovalCount(t, store, 0)
		})
	}
}

func TestMiddleware_500WhenIDFuncReturnsError(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store,
		headerActor(),
		WithIDFuncE(func() (string, error) {
			return "", errors.New("id failed")
		}),
	)
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", ""))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	requireApprovalCount(t, store, 0)
}

func TestMiddleware_413WhenBodyTooLarge(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor(), WithMaxBodyBytes(8))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "0123456789"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestMiddleware_BodyAtCapAccepted(t *testing.T) {
	// The "exactly at the cap" boundary case — exercising the off-by-
	// one we'd otherwise have between read-N+1 and len > N.
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor(), WithMaxBodyBytes(8))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "01234567"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestMiddleware_ActorExtraction(t *testing.T) {
	store := newApprovalStore(t)
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
	store := newApprovalStore(t)
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
	store := newApprovalStore(t)
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

func TestMiddleware_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() { Middleware(newApprovalStore(t), nil) })
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

func TestWithTenantFromHeader_PanicsOnInvalidName(t *testing.T) {
	assert.Panics(t, func() { WithTenantFromHeader("") })
	assert.Panics(t, func() { WithTenantFromHeader("Bad Header") })
}

func TestWithActorExtractor_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithActorExtractor(nil) })
}

func TestWithActorFromHeader_PanicsOnInvalidName(t *testing.T) {
	assert.Panics(t, func() { WithActorFromHeader("") })
	assert.Panics(t, func() { WithActorFromHeader("Bad Header") })
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

func TestWithIDFuncE_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithIDFuncE(nil) })
}

func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	store := newApprovalStore(t)
	mw := Middleware(store, headerActor(), WithLogger(nil))
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodPost, "/v1/x", "{}"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
}

func TestMiddleware_IDFuncLogRedactsError(t *testing.T) {
	store := newApprovalStore(t)
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	mw := Middleware(store,
		headerActor(),
		WithLogger(logger),
		WithIDFuncE(func() (string, error) {
			return "", errors.New("tenant-secret-id failed")
		}),
	)

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(http.MethodDelete, "/v1/users/42", "{}"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	out := buf.String()
	assert.Contains(t, out, `"error"`)
	assert.NotContains(t, out, "tenant-secret-id")
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

func TestEnsureBodyBuffered_DetachesCallerSlice(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/x", nil)
	body := []byte(`{"replayed":true}`)
	r2 := EnsureBodyBuffered(r, body)

	clear(body)

	got, err := io.ReadAll(r2.Body)
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"replayed":true}`), got)
	assert.Equal(t, int64(len(got)), r2.ContentLength)
}
