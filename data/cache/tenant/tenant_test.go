package tenant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/cache"
)

// fakeCache is a minimal in-memory cache for isolating the wrapper's
// behaviour from the real backends — we only care that the *exact*
// key reaches the inner store.
type fakeCache struct {
	store map[string][]byte
}

func newFakeCache() *fakeCache {
	return &fakeCache{store: make(map[string][]byte)}
}

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := f.store[key]
	if !ok {
		return nil, cache.ErrCacheMiss
	}
	return v, nil
}

func (f *fakeCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.store[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeCache) Delete(_ context.Context, key string) error {
	delete(f.store, key)
	return nil
}

func (f *fakeCache) Exists(_ context.Context, key string) (bool, error) {
	_, ok := f.store[key]
	return ok, nil
}

func ctxWith(tenantID string) context.Context {
	return coretenant.WithID(context.Background(), coretenant.ID(tenantID))
}

func TestWrap_IsolatesTenants(t *testing.T) {
	inner := newFakeCache()
	w := Wrap(inner)

	require.NoError(t, w.Set(ctxWith("acme"), "session", []byte("a-value"), time.Minute))
	require.NoError(t, w.Set(ctxWith("widgets"), "session", []byte("w-value"), time.Minute))

	got, err := w.Get(ctxWith("acme"), "session")
	require.NoError(t, err)
	assert.Equal(t, []byte("a-value"), got)

	got, err = w.Get(ctxWith("widgets"), "session")
	require.NoError(t, err)
	assert.Equal(t, []byte("w-value"), got)

	// Inner store must contain both fully-qualified keys.
	_, ok := inner.store["tenant:acme:session"]
	assert.True(t, ok, "expected acme key to exist in inner store")
	_, ok = inner.store["tenant:widgets:session"]
	assert.True(t, ok, "expected widgets key to exist in inner store")
}

func TestWrap_DeleteIsolatesTenants(t *testing.T) {
	inner := newFakeCache()
	w := Wrap(inner)

	require.NoError(t, w.Set(ctxWith("acme"), "k", []byte("a"), time.Minute))
	require.NoError(t, w.Set(ctxWith("widgets"), "k", []byte("w"), time.Minute))

	require.NoError(t, w.Delete(ctxWith("acme"), "k"))

	exists, err := w.Exists(ctxWith("acme"), "k")
	require.NoError(t, err)
	assert.False(t, exists, "acme key should have been deleted")

	exists, err = w.Exists(ctxWith("widgets"), "k")
	require.NoError(t, err)
	assert.True(t, exists, "widgets key should be untouched")
}

func TestWrap_PanicsOnMissingTenant(t *testing.T) {
	w := Wrap(newFakeCache())

	assertPanics := func(t *testing.T, name string, f func()) {
		t.Helper()
		defer func() {
			r := recover()
			assert.NotNilf(t, r, "%s did not panic", name)
		}()
		f()
	}

	assertPanics(t, "Get", func() { _, _ = w.Get(context.Background(), "k") })
	assertPanics(t, "Set", func() { _ = w.Set(context.Background(), "k", []byte("v"), time.Minute) })
	assertPanics(t, "Delete", func() { _ = w.Delete(context.Background(), "k") })
	assertPanics(t, "Exists", func() { _, _ = w.Exists(context.Background(), "k") })
}

func TestWrap_NilInnerPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil inner cache")
		}
	}()
	Wrap(nil)
}

func TestWrap_MissReachesCaller(t *testing.T) {
	w := Wrap(newFakeCache())
	_, err := w.Get(ctxWith("acme"), "missing")
	assert.True(t, errors.Is(err, cache.ErrCacheMiss), "expected ErrCacheMiss, got %v", err)
}
