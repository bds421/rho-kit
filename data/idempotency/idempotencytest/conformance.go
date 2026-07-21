package idempotencytest

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// Factory constructs a fresh Store for one subtest. It receives
// *testing.T so it can register cleanup via t.Cleanup if needed
// (closing a connection, dropping a table, etc.).
type Factory func(t *testing.T) idempotency.Store

// Run executes the full conformance battery against the
// supplied factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	if factory == nil {
		t.Fatal("idempotencytest.Run: factory must not be nil")
	}

	t.Run("RejectsEmptyKey", func(t *testing.T) { testRejectsEmptyKey(t, factory) })
	t.Run("RejectsOversizedKey", func(t *testing.T) { testRejectsOversizedKey(t, factory) })
	t.Run("RejectsInvalidKeyChars", func(t *testing.T) { testRejectsInvalidKeyChars(t, factory) })
	t.Run("RejectsInvalidTTL", func(t *testing.T) { testRejectsInvalidTTL(t, factory) })
	t.Run("LockSetGetRoundTrip", func(t *testing.T) { testLockSetGetRoundTrip(t, factory) })
	t.Run("FingerprintMatchPath", func(t *testing.T) { testFingerprintMatchPath(t, factory) })
	t.Run("FingerprintMismatchOnGet", func(t *testing.T) { testFingerprintMismatchOnGet(t, factory) })
	t.Run("FingerprintMismatchOnTryLock", func(t *testing.T) { testFingerprintMismatchOnTryLock(t, factory) })
	t.Run("EmptyFingerprintKeepsMismatchDetection", func(t *testing.T) { testEmptyFingerprintKeepsMismatchDetection(t, factory) })
	t.Run("ConcurrentTryLockSerializes", func(t *testing.T) { testConcurrentTryLockSerializes(t, factory) })
	t.Run("UnlockWithStaleTokenIsNoOp", func(t *testing.T) { testUnlockWithStaleTokenIsNoOp(t, factory) })
	t.Run("SetWithStaleTokenReturnsErrLockLost", func(t *testing.T) { testSetWithStaleTokenReturnsErrLockLost(t, factory) })
	t.Run("CachedResponseRoundTripsExactly", func(t *testing.T) { testCachedResponseRoundTripsExactly(t, factory) })
	t.Run("GetOnMissingKeyReturnsMiss", func(t *testing.T) { testGetOnMissingKeyReturnsMiss(t, factory) })
}

func testRejectsEmptyKey(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()

	_, _, err := s.Get(ctx, "", nil)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty, "Get must reject empty key")

	_, _, _, err = s.TryLock(ctx, "", nil, time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty, "TryLock must reject empty key")

	err = s.Set(ctx, "", "tok", idempotency.CachedResponse{StatusCode: 200}, time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty, "Set must reject empty key")

	err = s.Unlock(ctx, "", "tok")
	assert.ErrorIs(t, err, idempotency.ErrKeyEmpty, "Unlock must reject empty key")
}

func testRejectsOversizedKey(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()

	// A key one byte over the limit must be rejected by every method
	// with ErrKeyTooLong — a backend that silently truncates or stores
	// it would diverge from the kit's MemoryStore.
	oversized := strings.Repeat("a", idempotency.MaxKeyLen+1)

	_, _, err := s.Get(ctx, oversized, nil)
	assert.ErrorIs(t, err, idempotency.ErrKeyTooLong, "Get must reject oversized key")

	_, _, _, err = s.TryLock(ctx, oversized, nil, time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyTooLong, "TryLock must reject oversized key")

	err = s.Set(ctx, oversized, "tok", idempotency.CachedResponse{StatusCode: 200}, time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrKeyTooLong, "Set must reject oversized key")

	err = s.Unlock(ctx, oversized, "tok")
	assert.ErrorIs(t, err, idempotency.ErrKeyTooLong, "Unlock must reject oversized key")
}

func testRejectsInvalidKeyChars(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()

	// Control bytes, whitespace, and invalid UTF-8 must all surface as
	// ErrKeyInvalidChars so backends cannot silently accept keys that
	// corrupt logs or protocol framing (review-12).
	invalids := []string{
		"has\nnewline",
		"has\rreturn",
		"has\ttab",
		"has space",
		"has\x00nul",
		string([]byte{0xff, 0xfe, 'a'}),
	}
	for _, key := range invalids {
		_, _, err := s.Get(ctx, key, nil)
		assert.ErrorIs(t, err, idempotency.ErrKeyInvalidChars, "Get key %q", key)

		_, _, _, err = s.TryLock(ctx, key, nil, time.Minute)
		assert.ErrorIs(t, err, idempotency.ErrKeyInvalidChars, "TryLock key %q", key)

		err = s.Set(ctx, key, "tok", idempotency.CachedResponse{StatusCode: 200}, time.Minute)
		assert.ErrorIs(t, err, idempotency.ErrKeyInvalidChars, "Set key %q", key)

		err = s.Unlock(ctx, key, "tok")
		assert.ErrorIs(t, err, idempotency.ErrKeyInvalidChars, "Unlock key %q", key)
	}
}

func testRejectsInvalidTTL(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()

	_, _, _, err := s.TryLock(ctx, "k", nil, 0)
	assert.ErrorIs(t, err, idempotency.ErrInvalidTTL, "TryLock TTL=0 must return ErrInvalidTTL")

	_, _, _, err = s.TryLock(ctx, "k", nil, -time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrInvalidTTL, "TryLock negative TTL must return ErrInvalidTTL")

	// Set must reject TTL <= 0 too. Acquire a real lock first so the
	// only thing wrong with the Set call is the TTL — a backend that
	// creates an instantly-expired entry on Set(ttl=0) is the exact
	// divergence class this harness exists to catch.
	token, _, ok, err := s.TryLock(ctx, "k", nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "TryLock with a valid TTL must acquire")

	resp := idempotency.CachedResponse{StatusCode: 200}
	err = s.Set(ctx, "k", token, resp, 0)
	assert.ErrorIs(t, err, idempotency.ErrInvalidTTL, "Set TTL=0 must return ErrInvalidTTL")

	err = s.Set(ctx, "k", token, resp, -time.Minute)
	assert.ErrorIs(t, err, idempotency.ErrInvalidTTL, "Set negative TTL must return ErrInvalidTTL")
}

func testLockSetGetRoundTrip(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "round-trip-1"

	// Initial Get: miss.
	resp, mismatch, err := s.Get(ctx, key, nil)
	require.NoError(t, err)
	assert.False(t, mismatch, "miss must not signal mismatch")
	assert.Nil(t, resp, "miss returns nil resp")

	// TryLock succeeds.
	token, mismatch, ok, err := s.TryLock(ctx, key, nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "first TryLock must acquire")
	assert.False(t, mismatch)
	assert.NotEmpty(t, token, "successful TryLock returns a non-empty token")

	// Set the response.
	cached := idempotency.CachedResponse{
		StatusCode: 202,
		Headers:    map[string][]string{"X-Test": {"hello"}},
		Body:       []byte(`{"status":"accepted"}`),
	}
	require.NoError(t, s.Set(ctx, key, token, cached, time.Minute))

	// Get now hits.
	resp, mismatch, err = s.Get(ctx, key, nil)
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp, "Get after Set must return the cached response")
	assert.Equal(t, 202, resp.StatusCode)
	assert.Equal(t, []byte(`{"status":"accepted"}`), resp.Body)
}

func testFingerprintMatchPath(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "fp-match"
	fp := []byte("body-hash-abc")

	token, _, ok, err := s.TryLock(ctx, key, fp, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, s.Set(ctx, key, token, idempotency.CachedResponse{StatusCode: 200, Body: []byte("ok")}, time.Minute))

	resp, mismatch, err := s.Get(ctx, key, fp)
	require.NoError(t, err)
	assert.False(t, mismatch, "matching fingerprint must NOT signal mismatch")
	require.NotNil(t, resp)
}

func testFingerprintMismatchOnGet(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "fp-get-mismatch"

	token, _, ok, err := s.TryLock(ctx, key, []byte("first"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.Set(ctx, key, token, idempotency.CachedResponse{StatusCode: 200}, time.Minute))

	// Get with a DIFFERENT fingerprint must return mismatch=true,
	// resp=nil.
	resp, mismatch, err := s.Get(ctx, key, []byte("different"))
	require.NoError(t, err)
	assert.True(t, mismatch, "fingerprint mismatch on Get must signal true")
	assert.Nil(t, resp, "fingerprint mismatch hides the response")
}

func testFingerprintMismatchOnTryLock(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "fp-trylock-mismatch"

	// First caller takes the lock and writes a response.
	token, _, ok, err := s.TryLock(ctx, key, []byte("first"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.Set(ctx, key, token, idempotency.CachedResponse{StatusCode: 200}, time.Minute))

	// A second caller arrives with a different fingerprint —
	// must see mismatch=true.
	_, mismatch, ok, err := s.TryLock(ctx, key, []byte("different"), time.Minute)
	require.NoError(t, err)
	assert.True(t, mismatch, "fingerprint mismatch on TryLock must signal true (422 path)")
	assert.False(t, ok, "mismatch implies the lock was not granted")
}

// testEmptyFingerprintKeepsMismatchDetection pins the cross-backend contract
// for a *non-nil empty* fingerprint. A caller may legitimately supply
// []byte{} (e.g. a hash of an empty body). The empty fingerprint must be
// treated as a real, present fingerprint — not collapsed to nil — so that a
// later request bearing a *different*, non-empty fingerprint under the same
// key is still rejected with mismatch=true (the 422 path). Backends that
// internally clone the fingerprint must preserve its emptiness; turning
// []byte{} into nil silently disables mismatch detection for that key and
// diverges from the SQL bytea (non-NULL empty) semantics.
func testEmptyFingerprintKeepsMismatchDetection(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "fp-empty-nonnil"

	empty := []byte{}
	require.NotNil(t, empty, "test precondition: fingerprint must be empty but non-nil")

	// First caller claims the key with an empty (but present) fingerprint.
	token, _, ok, err := s.TryLock(ctx, key, empty, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.Set(ctx, key, token, idempotency.CachedResponse{StatusCode: 200}, time.Minute))

	// A retry with the SAME empty fingerprint is a match: Get replays.
	resp, mismatch, err := s.Get(ctx, key, empty)
	require.NoError(t, err)
	assert.False(t, mismatch, "empty fingerprint matching empty fingerprint must NOT signal mismatch")
	require.NotNil(t, resp, "matching empty fingerprint must replay the cached response")

	// A request with a DIFFERENT, non-empty fingerprint under the same key
	// must still be caught as a mismatch on Get.
	resp, mismatch, err = s.Get(ctx, key, []byte("different"))
	require.NoError(t, err)
	assert.True(t, mismatch, "different fingerprint vs stored empty fingerprint must signal mismatch on Get")
	assert.Nil(t, resp, "fingerprint mismatch hides the response")
}

func testConcurrentTryLockSerializes(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "concurrent"

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, ok, err := s.TryLock(ctx, key, nil, time.Minute)
			if err == nil && ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), winners.Load(), "exactly one caller must win the concurrent TryLock race")
}

func testUnlockWithStaleTokenIsNoOp(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "stale-unlock"

	token, _, ok, err := s.TryLock(ctx, key, nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Unlock with the right token: succeeds.
	require.NoError(t, s.Unlock(ctx, key, token))

	// Unlock again with the same token: now stale; MUST be a no-op.
	err = s.Unlock(ctx, key, token)
	require.NoError(t, err, "Unlock is best-effort cleanup; stale token must NOT surface ErrLockLost")

	// Unlock with a totally different token: also a no-op.
	err = s.Unlock(ctx, key, "never-existed")
	require.NoError(t, err, "Unlock with unknown token must be a no-op")
}

func testSetWithStaleTokenReturnsErrLockLost(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "set-stale"

	// Caller A locks.
	tokenA, _, ok, err := s.TryLock(ctx, key, nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Caller A unlocks (simulating panic / abandon) — now the
	// lock row is free.
	require.NoError(t, s.Unlock(ctx, key, tokenA))

	// Caller B takes the same key.
	tokenB, _, okB, err := s.TryLock(ctx, key, nil, time.Minute)
	require.NoError(t, err)
	require.True(t, okB)
	require.NotEqual(t, tokenA, tokenB, "tokens MUST be unique across consecutive owners")

	// Caller A now tries to Set with their old token — must fail
	// closed with ErrLockLost so A doesn't clobber B's in-progress
	// processing.
	err = s.Set(ctx, key, tokenA, idempotency.CachedResponse{StatusCode: 500}, time.Minute)
	if err == nil {
		t.Fatal("Set with a stolen-and-replaced token must return ErrLockLost; got nil")
	}
	assert.ErrorIs(t, err, idempotency.ErrLockLost,
		"Set with a stale token must return ErrLockLost, got %v", err)
}

func testCachedResponseRoundTripsExactly(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()
	const key = "fields-roundtrip"

	token, _, ok, err := s.TryLock(ctx, key, nil, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	cached := idempotency.CachedResponse{
		StatusCode: http.StatusAccepted,
		Headers: map[string][]string{
			"X-Single":      {"v1"},
			"X-Multi-Value": {"a", "b", "c"},
		},
		Body: []byte("payload-bytes"),
	}
	require.NoError(t, s.Set(ctx, key, token, cached, time.Minute))

	resp, _, err := s.Get(ctx, key, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, cached.StatusCode, resp.StatusCode)
	assert.Equal(t, cached.Body, resp.Body)
	assert.Equal(t, cached.Headers["X-Single"], resp.Headers["X-Single"])
	assert.Equal(t, cached.Headers["X-Multi-Value"], resp.Headers["X-Multi-Value"],
		"multi-value headers must round-trip with order preserved")
}

func testGetOnMissingKeyReturnsMiss(t *testing.T, factory Factory) {
	s := factory(t)
	ctx := context.Background()

	resp, mismatch, err := s.Get(context.Background(), "never-set", nil)
	require.NoError(t, err)
	assert.False(t, mismatch, "missing key must NOT signal mismatch")
	assert.Nil(t, resp, "missing key must return nil resp")

	// Same with a fingerprint — still miss, not mismatch.
	resp, mismatch, err = s.Get(ctx, "never-set", []byte("anything"))
	require.NoError(t, err)
	assert.False(t, mismatch, "missing key must NOT signal mismatch even with fingerprint")
	assert.Nil(t, resp)

}
