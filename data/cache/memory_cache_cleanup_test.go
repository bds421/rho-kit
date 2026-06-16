package cache

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeForgottenCache builds a MemoryCache, returns only its ristrettoCloser,
// and lets the *MemoryCache go out of scope so it becomes eligible for GC.
// Returning the closer (which never references the MemoryCache) keeps the
// underlying ristretto store observable without pinning the cache itself.
func makeForgottenCache(t *testing.T) *ristrettoCloser {
	t.Helper()
	mc, err := NewMemoryCache()
	require.NoError(t, err)
	closer := mc.closer
	require.NotNil(t, closer)
	require.NotNil(t, closer.cache)
	// Sanity: the store is open right now (SetWithTTL succeeds).
	require.True(t, closer.cache.SetWithTTL("probe", []byte("v"), 1, time.Minute),
		"freshly constructed ristretto store must accept writes")
	// Do not return mc — it is now unreferenced and collectable.
	return closer
}

// TestMemoryCache_ForgottenClose_WatchdogClosesRistretto verifies the
// runtime.AddCleanup watchdog: when a caller forgets Close and drops the
// MemoryCache, the watchdog eventually closes the underlying ristretto store
// (stopping its processItems goroutine and cleanup ticker) rather than
// leaking them for the process lifetime. ristretto.SetWithTTL returns false
// once the store is closed, which is the observable signal.
func TestMemoryCache_ForgottenClose_WatchdogClosesRistretto(t *testing.T) {
	closer := makeForgottenCache(t)

	// Force GC repeatedly so the unreachable MemoryCache is reclaimed and its
	// registered cleanup runs. Cleanups run asynchronously, so poll within a
	// generous budget.
	deadline := time.Now().Add(5 * time.Second)
	closed := false
	for time.Now().Before(deadline) {
		runtime.GC()
		// A closed ristretto store rejects writes (isClosed guard).
		if !closer.cache.SetWithTTL("probe2", []byte("v"), 1, time.Minute) {
			closed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.True(t, closed,
		"AddCleanup watchdog should close the ristretto store after the MemoryCache is GC'd")
}

// TestMemoryCache_ExplicitCloseThenWatchdog verifies the watchdog and the
// explicit Close path coexist: an explicit Close closes the store once, and a
// later watchdog firing (after GC) is a harmless no-op (closer.once).
func TestMemoryCache_ExplicitCloseThenWatchdog(t *testing.T) {
	mc, err := NewMemoryCache()
	require.NoError(t, err)
	closer := mc.closer

	require.NoError(t, mc.Close())
	// Store is closed after explicit Close.
	assert.False(t, closer.cache.SetWithTTL("k", []byte("v"), 1, time.Minute))

	// Closing again (idempotent) and a subsequent watchdog firing must not
	// panic. close() is guarded by sync.Once.
	require.NoError(t, mc.Close())
	assert.NotPanics(t, func() { closer.close() })
}
