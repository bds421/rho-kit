package pprof

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_ServesIndex(t *testing.T) {
	srv := httptest.NewServer(Handler(WithUnsafePublicMount()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_DefaultAllowsLoopback(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_DefaultRejectsNonLoopback(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_ServesNamedProfile(t *testing.T) {
	srv := httptest.NewServer(Handler(WithUnsafePublicMount()))
	defer srv.Close()

	// "goroutine" is a named profile that returns the running goroutines.
	resp, err := http.Get(srv.URL + "/debug/pprof/goroutine?debug=1")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMount_AttachesRoutesToCallerMux(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMountWith_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		MountWith(http.NewServeMux(), nil)
	})
}

func TestWithAuth_PanicsOnNilFunction(t *testing.T) {
	assert.Panics(t, func() {
		WithAuth(nil)
	})
}

func TestWithAuth_PanicFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	MountWith(mux, WithAuth(func(*http.Request) bool {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		mux.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestIsPprofPath(t *testing.T) {
	assert.True(t, IsPprofPath("/debug/pprof"))
	assert.True(t, IsPprofPath("/debug/pprof/"))
	assert.True(t, IsPprofPath("/debug/pprof/heap"))
	assert.True(t, IsPprofPath("/debug/pprof/goroutine?debug=1"))
	assert.False(t, IsPprofPath("/debug/metrics"))
	assert.False(t, IsPprofPath("/api/users"))
	// Look-alike paths must NOT match — a stricter check than HasPrefix.
	assert.False(t, IsPprofPath("/debug/pprofevil"))
	assert.False(t, IsPprofPath("/debug/pprof_secret"))
}

func TestEnableMutexBlockProfiling_DoesNotPanic(t *testing.T) {
	// We can't easily verify the runtime side effects but enabling and
	// disabling must be a no-throw operation across reasonable inputs.
	EnableMutexBlockProfiling(0, 0)
	EnableMutexBlockProfiling(100, 10_000_000)
	EnableMutexBlockProfiling(0, 0) // reset
}
