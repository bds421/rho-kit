package tenant

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/cache"
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

	// Inner store must contain both fully-qualified keys. Variable
	// fields are length-prefixed by core/tenant.Key — see scopedKey for
	// the rationale.
	_, ok := inner.store["tenant:4:acme:7:session"]
	assert.True(t, ok, "expected acme key to exist in inner store")
	_, ok = inner.store["tenant:7:widgets:7:session"]
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

func TestWrap_MissingTenantReturnsError(t *testing.T) {
	w := Wrap(newFakeCache())

	_, err := w.Get(context.Background(), "k")
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	err = w.Set(context.Background(), "k", []byte("v"), time.Minute)
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	err = w.Delete(context.Background(), "k")
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	exists, err := w.Exists(context.Background(), "k")
	assert.False(t, exists)
	assert.ErrorIs(t, err, coretenant.ErrMissing)
}

func TestWrap_RejectsEmptyRawKey(t *testing.T) {
	w := Wrap(newFakeCache())
	ctx := ctxWith("acme")

	_, err := w.Get(ctx, "")
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)

	err = w.Set(ctx, "", []byte("v"), time.Minute)
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)

	err = w.Delete(ctx, "")
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)

	exists, err := w.Exists(ctx, "")
	assert.False(t, exists)
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)
}

func TestWrap_RejectsScopedKeyTooLong(t *testing.T) {
	inner := newFakeCache()
	w := Wrap(inner)
	ctx := coretenant.WithID(context.Background(), coretenant.NewIDUnchecked(strings.Repeat("t", coretenant.MaxIDLen)))

	err := w.Set(ctx, strings.Repeat("k", cache.MaxKeyLen), []byte("v"), time.Minute)
	assert.ErrorIs(t, err, cache.ErrKeyTooLong)
	assert.Empty(t, inner.store, "oversized scoped keys must not reach the inner cache")
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

func TestScoped_InvalidReceiverReturnsError(t *testing.T) {
	ctx := ctxWith("acme")

	for name, s := range map[string]*scoped{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Get(ctx, "key")
			assert.ErrorIs(t, err, cache.ErrInvalidCache)

			err = s.Set(ctx, "key", []byte("value"), time.Minute)
			assert.ErrorIs(t, err, cache.ErrInvalidCache)

			err = s.Delete(ctx, "key")
			assert.ErrorIs(t, err, cache.ErrInvalidCache)

			exists, err := s.Exists(ctx, "key")
			assert.False(t, exists)
			assert.ErrorIs(t, err, cache.ErrInvalidCache)
		})
	}
}

// fakeBulkCache extends fakeCache with the BulkCache interface so we
// can verify that Wrap preserves bulk/CAS semantics when the inner
// cache supports them.
type fakeBulkCache struct {
	*fakeCache
	mgetCalls  int
	msetCalls  int
	setNXCalls int
}

func newFakeBulkCache() *fakeBulkCache {
	return &fakeBulkCache{fakeCache: newFakeCache()}
}

func (f *fakeBulkCache) MGet(_ context.Context, keys []string) (map[string][]byte, error) {
	f.mgetCalls++
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		if v, ok := f.store[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeBulkCache) MSet(_ context.Context, items map[string][]byte, _ time.Duration) error {
	f.msetCalls++
	for k, v := range items {
		f.store[k] = append([]byte(nil), v...)
	}
	return nil
}

func (f *fakeBulkCache) SetNX(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	f.setNXCalls++
	if _, ok := f.store[key]; ok {
		return false, nil
	}
	f.store[key] = append([]byte(nil), value...)
	return true, nil
}

// TestWrap_PreservesBulkCache verifies that Wrap on a BulkCache returns
// a BulkCache whose MGet/MSet/SetNX route to the inner bulk methods
// with tenant-prefixed keys. Before the v2 audit fix, the wrapper
// returned cache.Cache and MGet/MSet/SetNX silently fell back to the
// per-key racy helpers.
func TestWrap_PreservesBulkCache(t *testing.T) {
	inner := newFakeBulkCache()
	w := Wrap(inner)

	bulk, ok := w.(cache.BulkCache)
	require.True(t, ok, "wrapped BulkCache must satisfy cache.BulkCache")

	ctx := ctxWith("acme")

	require.NoError(t, bulk.MSet(ctx, map[string][]byte{
		"k1": []byte("v1"),
		"k2": []byte("v2"),
	}, time.Minute))
	assert.Equal(t, 1, inner.msetCalls, "MSet must reach inner BulkCache once")

	_, ok1 := inner.store["tenant:4:acme:2:k1"]
	_, ok2 := inner.store["tenant:4:acme:2:k2"]
	assert.True(t, ok1 && ok2, "MSet must store tenant-scoped keys in inner")

	got, err := bulk.MGet(ctx, []string{"k1", "k2", "missing"})
	require.NoError(t, err)
	assert.Equal(t, 1, inner.mgetCalls, "MGet must reach inner BulkCache once")
	assert.Equal(t, []byte("v1"), got["k1"])
	assert.Equal(t, []byte("v2"), got["k2"])
	_, hasMissing := got["missing"]
	assert.False(t, hasMissing, "MGet must not invent missing keys")

	ok, err = bulk.SetNX(ctx, "claim", []byte("first"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 1, inner.setNXCalls, "SetNX must reach inner BulkCache")

	ok, err = bulk.SetNX(ctx, "claim", []byte("second"), time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "second SetNX must fail")

	// Different tenant must be able to claim the same raw key.
	ok, err = bulk.SetNX(ctxWith("widgets"), "claim", []byte("widgets-claim"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "different tenant must not collide with prior claim")
}

func TestScopedBulk_InvalidReceiverReturnsError(t *testing.T) {
	ctx := ctxWith("acme")

	for name, s := range map[string]*scopedBulk{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.MGet(ctx, []string{"key"})
			assert.ErrorIs(t, err, cache.ErrInvalidCache)

			err = s.MSet(ctx, map[string][]byte{"key": []byte("value")}, time.Minute)
			assert.ErrorIs(t, err, cache.ErrInvalidCache)

			ok, err := s.SetNX(ctx, "key", []byte("value"), time.Minute)
			assert.False(t, ok)
			assert.ErrorIs(t, err, cache.ErrInvalidCache)
		})
	}
}

func TestScopedBulk_RejectsEmptyRawKey(t *testing.T) {
	bulk := Wrap(newFakeBulkCache()).(cache.BulkCache)
	ctx := ctxWith("acme")

	_, err := bulk.MGet(ctx, []string{"valid", ""})
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)

	err = bulk.MSet(ctx, map[string][]byte{"": []byte("value")}, time.Minute)
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)

	ok, err := bulk.SetNX(ctx, "", []byte("value"), time.Minute)
	assert.False(t, ok)
	assert.ErrorIs(t, err, cache.ErrKeyEmpty)
}

func oversizedTenantKeysForTest() []string {
	keys := make([]string, cache.MaxBulkKeys+1)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	return keys
}

func oversizedTenantItemsForTest() map[string][]byte {
	items := make(map[string][]byte, cache.MaxBulkKeys+1)
	for i := 0; i <= cache.MaxBulkKeys; i++ {
		items["key-"+strconv.Itoa(i)] = []byte("value")
	}
	return items
}

func TestScopedBulk_RejectsOversizedBatchesBeforeScoping(t *testing.T) {
	inner := newFakeBulkCache()
	bulk := Wrap(inner).(cache.BulkCache)
	ctx := ctxWith("acme")

	_, err := bulk.MGet(ctx, oversizedTenantKeysForTest())
	assert.ErrorIs(t, err, cache.ErrBulkTooLarge)
	assert.Equal(t, 0, inner.mgetCalls)

	err = bulk.MSet(ctx, oversizedTenantItemsForTest(), time.Minute)
	assert.ErrorIs(t, err, cache.ErrBulkTooLarge)
	assert.Equal(t, 0, inner.msetCalls)
}

// TestWrap_NonBulkInner_KeepsCacheOnly verifies that wrapping a plain
// Cache does not synthesize a fake BulkCache — the type assertion in
// callers should fail and they should keep using the per-key fallback.
func TestWrap_NonBulkInner_KeepsCacheOnly(t *testing.T) {
	inner := newFakeCache()
	w := Wrap(inner)

	_, isBulk := w.(cache.BulkCache)
	assert.False(t, isBulk, "wrapping a plain Cache must not synthesize BulkCache")
}

// TestScopedKey_ColonInTenantIDNoCollision is the audit's exact test
// case for C-3. With a naive `tenant:<id>:<key>` scheme, tenant `"a:b"`
// with key `"c"` collides with tenant `"a"` with key `"b:c"` — both
// stringify to `tenant:a:b:c` and tenant B can read tenant A's data.
//
// We use NewIDUnchecked to simulate the worst case where validation is
// bypassed (e.g. legacy data, direct ID conversion). The length prefix
// in scopedKey must keep the namespaces disjoint regardless.
func TestScopedKey_ColonInTenantIDNoCollision(t *testing.T) {
	inner := newFakeCache()
	w := Wrap(inner)

	idAB := coretenant.NewIDUnchecked("a:b")
	idA := coretenant.NewIDUnchecked("a")

	ctxAB := coretenant.WithID(context.Background(), idAB)
	ctxA := coretenant.WithID(context.Background(), idA)

	require.NoError(t, w.Set(ctxAB, "c", []byte("ab-tenant"), time.Minute))
	require.NoError(t, w.Set(ctxA, "b:c", []byte("a-tenant"), time.Minute))

	// Each tenant must read back its own value.
	got, err := w.Get(ctxAB, "c")
	require.NoError(t, err)
	assert.Equal(t, []byte("ab-tenant"), got, "tenant a:b read leaked from tenant a")

	got, err = w.Get(ctxA, "b:c")
	require.NoError(t, err)
	assert.Equal(t, []byte("a-tenant"), got, "tenant a read leaked from tenant a:b")

	// Inner store should hold two distinct keys, not one.
	assert.Len(t, inner.store, 2, "expected two distinct scoped keys")
}
