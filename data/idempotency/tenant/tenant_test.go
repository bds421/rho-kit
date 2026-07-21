package tenant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

func ctxWith(tenantID string) context.Context {
	ctx, err := coretenant.WithID(context.Background(), coretenant.ID(tenantID))
	if err != nil {
		panic(err)
	}
	return ctx
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

func TestWrap_MissingTenantReturnsError(t *testing.T) {
	w := Wrap(idempotency.NewMemoryStore())

	resp, mismatch, err := w.Get(context.Background(), "k", nil)
	assert.Nil(t, resp)
	assert.False(t, mismatch)
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	token, mismatch, ok, err := w.TryLock(context.Background(), "k", nil, time.Minute)
	assert.Empty(t, token)
	assert.False(t, mismatch)
	assert.False(t, ok)
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	err = w.Set(context.Background(), "k", "t", idempotency.CachedResponse{}, time.Minute)
	assert.ErrorIs(t, err, coretenant.ErrMissing)

	err = w.Unlock(context.Background(), "k", "t")
	assert.ErrorIs(t, err, coretenant.ErrMissing)
}

func TestWrap_RejectsEmptyRawKey(t *testing.T) {
	w := Wrap(idempotency.NewMemoryStore())
	ctx := ctxWith("acme")

	resp, mismatch, err := w.Get(ctx, "", nil)
	assert.Nil(t, resp)
	assert.False(t, mismatch)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty)

	token, mismatch, ok, err := w.TryLock(ctx, "", nil, time.Minute)
	assert.Empty(t, token)
	assert.False(t, mismatch)
	assert.False(t, ok)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty)

	err = w.Set(ctx, "", "t", idempotency.CachedResponse{}, time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty)

	err = w.Unlock(ctx, "", "t")
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty)
}

func TestWrap_HashesWhenScopedExceedsMaxKeyLen(t *testing.T) {
	// Long tenant IDs + long raw keys used to fail closed with ErrKeyTooLong
	// after prefixing. The wrapper now hashes the scoped form so every
	// ValidateKey-passing raw key remains usable (review-12).
	w := Wrap(idempotency.NewMemoryStore())
	ctx, ctxErr := coretenant.WithID(context.Background(), coretenant.MustNewID(strings.Repeat("t", coretenant.MaxIDLen)))
	require.NoError(t, ctxErr)

	raw := strings.Repeat("k", idempotency.MaxKeyLen)
	token, mismatch, ok, err := w.TryLock(ctx, raw, nil, time.Minute)
	require.NoError(t, err)
	assert.False(t, mismatch)
	assert.True(t, ok)
	assert.NotEmpty(t, token)

	require.NoError(t, w.Set(ctx, raw, token, idempotency.CachedResponse{
		StatusCode: 200,
		Body:       []byte("hashed-ok"),
	}, time.Minute))
	resp, mismatch, err := w.Get(ctx, raw, nil)
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp)
	assert.Equal(t, []byte("hashed-ok"), resp.Body)
}

func TestWrap_NilInnerPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil inner store")
		}
	}()
	Wrap(nil)
}

func TestScoped_InvalidReceiverReturnsError(t *testing.T) {
	ctx := ctxWith("acme")

	for name, store := range map[string]*scoped{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			resp, mismatch, err := store.Get(ctx, "k", nil)
			assert.Nil(t, resp)
			assert.False(t, mismatch)
			assert.ErrorIs(t, err, idempotency.ErrInvalidStore)

			token, mismatch, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
			assert.Empty(t, token)
			assert.False(t, mismatch)
			assert.False(t, ok)
			assert.ErrorIs(t, err, idempotency.ErrInvalidStore)

			err = store.Set(ctx, "k", "t", idempotency.CachedResponse{}, time.Minute)
			assert.ErrorIs(t, err, idempotency.ErrInvalidStore)

			err = store.Unlock(ctx, "k", "t")
			assert.ErrorIs(t, err, idempotency.ErrInvalidStore)
		})
	}
}

// TestScopedKey_ColonInTenantIDNoCollision is the audit's exact test
// case for C-3. With a naive `tenant:<id>:<key>` scheme, tenant `"a:b"`
// taking key `"c"` would share a storage slot with tenant `"a"` taking
// key `"b:c"` — opening the door to cross-tenant idempotency replay.
//
// MustNewID simulates the worst case where validation is bypassed
// (e.g. legacy data). The length prefix in scopedKey must keep the
// namespaces disjoint regardless.
func TestScopedKey_ColonInTenantIDNoCollision(t *testing.T) {
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	idAB := coretenant.IDFromTrusted("a:b")
	idA := coretenant.IDFromTrusted("a")

	ctxAB, err := coretenant.WithID(context.Background(), idAB)
	require.NoError(t, err)
	ctxA, err := coretenant.WithID(context.Background(), idA)
	require.NoError(t, err)

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

func TestWrap_LongTenantAndKeyStillWorks(t *testing.T) {
	// Raw key near MaxKeyLen + long tenant ID used to fail closed with
	// ErrKeyTooLong after prefixing; scoped keys are now hashed when they
	// exceed MaxKeyLen so the Store contract holds for every tenant.
	inner := idempotency.NewMemoryStore()
	w := Wrap(inner)

	tenantID := strings.Repeat("t", 100)
	rawKey := strings.Repeat("k", 200)
	ctx := ctxWith(tenantID)

	tok, _, ok, err := w.TryLock(ctx, rawKey, []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, w.Set(ctx, rawKey, tok, idempotency.CachedResponse{
		StatusCode: 200,
		Body:       []byte("ok"),
	}, time.Minute))

	resp, mismatch, err := w.Get(ctx, rawKey, []byte("body"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp)
	assert.Equal(t, []byte("ok"), resp.Body)
}

func TestWrap_BareStoreCannotForgeTenantSlot(t *testing.T) {
	// Shared backend: bare store + tenant wrapper. An attacker who knows
	// the length-prefixed form (or guesses it) must not be able to read
	// or squat on the tenant-wrapped slot via the bare store.
	inner := idempotency.NewMemoryStore()
	wrapped := Wrap(inner)

	ctx := ctxWith("acme")
	raw := "order-42"

	tok, _, ok, err := wrapped.TryLock(ctx, raw, []byte("body"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, wrapped.Set(ctx, raw, tok, idempotency.CachedResponse{
		StatusCode: 200,
		Body:       []byte("secret-tenant-response"),
	}, time.Minute))

	// 1. Length-prefixed coretenant form is rejected as a user/storage key.
	scopedReadable, err := coretenant.KeyFor(coretenant.MustNewID("acme"), raw)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(scopedReadable, "tenant:"))

	resp, mismatch, err := inner.Get(context.Background(), scopedReadable, []byte("body"))
	assert.ErrorIs(t, err, idempotency.ErrKeyReservedPrefix)
	assert.Nil(t, resp)
	assert.False(t, mismatch)

	_, _, ok, err = inner.TryLock(context.Background(), scopedReadable, []byte("body"), time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyReservedPrefix)
	assert.False(t, ok)

	// 2. Reserved tns: prefix as a raw user key is also rejected by ValidateKey
	// when used outside the storage path... but the bare store uses
	// ValidateStorageKey, which accepts well-formed tns: keys. Even then,
	// without knowing the exact digest of KeyFor(tenant, raw) the attacker
	// cannot target the slot. Prove a wrong digest misses.
	wrong := idempotency.TenantStorageKeyPrefix + strings.Repeat("00", 32)
	resp, mismatch, err = inner.Get(context.Background(), wrong, []byte("body"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	assert.Nil(t, resp, "wrong tns digest must miss the tenant slot")

	// 3. Tenant wrapper still reads its own response.
	resp, mismatch, err = wrapped.Get(ctx, raw, []byte("body"))
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp)
	assert.Equal(t, []byte("secret-tenant-response"), resp.Body)
}

func TestWrap_StorageKeyIsAlwaysOpaqueTns(t *testing.T) {
	// Capture keys the inner store sees.
	inner := &keyProbe{Store: idempotency.NewMemoryStore()}
	w := Wrap(inner)

	ctx := ctxWith("acme")
	_, _, ok, err := w.TryLock(ctx, "short", nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, inner.lastKey)
	assert.True(t, strings.HasPrefix(inner.lastKey, idempotency.TenantStorageKeyPrefix),
		"storage key must use tns: prefix, got %q", inner.lastKey)
	assert.False(t, strings.HasPrefix(inner.lastKey, "tenant:"),
		"storage key must not be the readable tenant: form")
	assert.Len(t, inner.lastKey, len(idempotency.TenantStorageKeyPrefix)+64)
}

// keyProbe records the last storage key seen by the inner store.
type keyProbe struct {
	idempotency.Store
	lastKey string
}

func (p *keyProbe) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	p.lastKey = key
	return p.Store.TryLock(ctx, key, fingerprint, ttl)
}

func (p *keyProbe) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	p.lastKey = key
	return p.Store.Get(ctx, key, fingerprint)
}

func (p *keyProbe) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	p.lastKey = key
	return p.Store.Set(ctx, key, token, resp, ttl)
}

func (p *keyProbe) Unlock(ctx context.Context, key, token string) error {
	p.lastKey = key
	return p.Store.Unlock(ctx, key, token)
}
