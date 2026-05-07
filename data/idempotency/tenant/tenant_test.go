package tenant

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/data/idempotency"
)

func ctxWith(tenantID string) context.Context {
	return coretenant.WithID(context.Background(), coretenant.ID(tenantID))
}

func TestWrap_IsolatesTenants_TryLock(t *testing.T) {
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	// Tenant A acquires a lock on key "k".
	tokA, mismatchA, okA, err := w.TryLock(ctxWith("acme"), "k", []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, okA, "tenant A should have acquired the lock")
	require.False(t, mismatchA)
	require.NotEmpty(t, tokA)

	// Tenant B acquires a lock on the same raw key — must succeed
	// because the keys live in different namespaces.
	tokB, mismatchB, okB, err := w.TryLock(ctxWith("widgets"), "k", []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, okB, "tenant B should also acquire its own lock on the same raw key")
	require.False(t, mismatchB)
	require.NotEmpty(t, tokB)

	// A second TryLock for tenant A on the same key must report
	// contention (its own lock).
	_, _, okA2, err := w.TryLock(ctxWith("acme"), "k", []byte("body"), time.Minute)
	require.NoError(t, err)
	assert.False(t, okA2, "tenant A's second TryLock should be contended")
}

func TestWrap_IsolatesTenants_Get(t *testing.T) {
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	// Acquire and write a response for tenant A.
	tokA, _, ok, err := w.TryLock(ctxWith("acme"), "k", []byte("b"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, w.Set(ctxWith("acme"), "k", tokA, idempotency.CachedResponse{
		StatusCode: 201,
		Body:       []byte("acme-response"),
	}, time.Minute))

	// Tenant B reads the same raw key — must miss because the lock /
	// response live in tenant A's namespace.
	resp, mismatch, err := w.Get(ctxWith("widgets"), "k", []byte("b"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	assert.Nil(t, resp, "tenant B must not see tenant A's cached response")

	// Tenant A reads its own response.
	resp, mismatch, err = w.Get(ctxWith("acme"), "k", []byte("b"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp)
	assert.Equal(t, []byte("acme-response"), resp.Body)
}

func TestWrap_PanicsOnMissingTenant(t *testing.T) {
	w := Wrap(idempotency.NewMemoryStore())

	assertPanics := func(t *testing.T, name string, f func()) {
		t.Helper()
		defer func() {
			r := recover()
			assert.NotNilf(t, r, "%s did not panic", name)
		}()
		f()
	}

	assertPanics(t, "Get", func() { _, _, _ = w.Get(context.Background(), "k", nil) })
	assertPanics(t, "TryLock", func() {
		_, _, _, _ = w.TryLock(context.Background(), "k", nil, time.Minute)
	})
	assertPanics(t, "Set", func() {
		_ = w.Set(context.Background(), "k", "t", idempotency.CachedResponse{}, time.Minute)
	})
	assertPanics(t, "Unlock", func() { _ = w.Unlock(context.Background(), "k", "t") })
}

func TestWrap_NilInnerPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil inner store")
		}
	}()
	Wrap(nil)
}

// TestScopedKey_ColonInTenantIDNoCollision is the audit's exact test
// case for C-3. With a naive `tenant:<id>:<key>` scheme, tenant `"a:b"`
// taking key `"c"` would share a storage slot with tenant `"a"` taking
// key `"b:c"` — opening the door to cross-tenant idempotency replay.
//
// NewIDUnchecked simulates the worst case where validation is bypassed
// (e.g. legacy data). The length prefix in scopedKey must keep the
// namespaces disjoint regardless.
func TestScopedKey_ColonInTenantIDNoCollision(t *testing.T) {
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	idAB := coretenant.NewIDUnchecked("a:b")
	idA := coretenant.NewIDUnchecked("a")

	ctxAB := coretenant.WithID(context.Background(), idAB)
	ctxA := coretenant.WithID(context.Background(), idA)

	// Tenant "a:b" locks key "c".
	tokAB, _, ok, err := w.TryLock(ctxAB, "c", []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "tenant a:b should acquire its lock")

	// Tenant "a" locks key "b:c". Naive scoping would treat this as a
	// re-lock of the same slot and contend; with the length prefix the
	// two namespaces are distinct so tenant "a" must succeed.
	tokA, _, ok, err := w.TryLock(ctxA, "b:c", []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "tenant a should acquire its lock independently of tenant a:b")

	// Cross-namespace reads must miss.
	require.NoError(t, w.Set(ctxAB, "c", tokAB, idempotency.CachedResponse{
		StatusCode: 200,
		Body:       []byte("ab-response"),
	}, time.Minute))
	resp, mismatch, err := w.Get(ctxA, "b:c", []byte("body"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	assert.Nil(t, resp, "tenant a must not see tenant a:b's response")

	_ = tokA
}

func TestWrap_UnlockReleasesOnlyOwnTenantLock(t *testing.T) {
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	tokA, _, ok, err := w.TryLock(ctxWith("acme"), "k", []byte("b"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	tokB, _, ok, err := w.TryLock(ctxWith("widgets"), "k", []byte("b"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Tenant A unlocks. Tenant B's lock must remain held.
	require.NoError(t, w.Unlock(ctxWith("acme"), "k", tokA))

	// Tenant A can re-acquire (its lock was released).
	_, _, ok, err = w.TryLock(ctxWith("acme"), "k", []byte("b"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "tenant A should re-acquire after Unlock")

	// Tenant B's lock is still held — TryLock again should be contended.
	_, _, ok, err = w.TryLock(ctxWith("widgets"), "k", []byte("b"), time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "tenant B's lock should still be held")
	_ = tokB
}
