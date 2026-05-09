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
