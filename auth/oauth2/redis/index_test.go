package redis_test

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisoauth "github.com/bds421/rho-kit/auth/oauth2/redis/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

func lookup(t *testing.T, fn func(context.Context, string) ([]string, error), key string) []string {
	t.Helper()
	ids, err := fn(context.Background(), key)
	require.NoError(t, err)
	sort.Strings(ids)
	return ids
}

func TestSessionIndex_MaintainedOnPutUpdateDelete(t *testing.T) {
	mr, store := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{UserID: "u1", SID: "sid-a"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s2", oauth2.Session{UserID: "u1"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s3", oauth2.Session{UserID: "u2", SID: "sid-b"}, time.Minute))

	assert.Equal(t, []string{"s1", "s2"}, lookup(t, store.SessionIDsBySubject, "u1"))
	assert.Equal(t, []string{"s1"}, lookup(t, store.SessionIDsBySID, "sid-a"))

	// Index keys carry no user identifiers.
	for _, k := range mr.Keys() {
		assert.NotContains(t, k, "u1")
		assert.NotContains(t, k, "sid-a")
	}

	// Update with a new SID moves the SID index entry.
	ok, err := store.Update(ctx, "s1", 0, oauth2.Session{UserID: "u1", SID: "sid-c", Version: 1}, time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, lookup(t, store.SessionIDsBySID, "sid-a"))
	assert.Equal(t, []string{"s1"}, lookup(t, store.SessionIDsBySID, "sid-c"))

	// Delete removes both entries.
	require.NoError(t, store.Delete(ctx, "s1"))
	assert.Equal(t, []string{"s2"}, lookup(t, store.SessionIDsBySubject, "u1"))
	assert.Empty(t, lookup(t, store.SessionIDsBySID, "sid-c"))

	// An expired session is filtered and pruned; the index set itself expires
	// no earlier than its longest-lived session.
	mr.FastForward(2 * time.Minute)
	assert.Empty(t, lookup(t, store.SessionIDsBySubject, "u2"))
	assert.Equal(t, []string{"s2"}, lookup(t, store.SessionIDsBySubject, "u1"))

	_, err = store.SessionIDsBySubject(ctx, "")
	require.Error(t, err)
}

func TestSessionIndex_ShortSessionDoesNotShortenIndex(t *testing.T) {
	mr, store := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "long", oauth2.Session{UserID: "u1"}, time.Hour))
	require.NoError(t, store.Put(ctx, "short", oauth2.Session{UserID: "u1"}, time.Minute))
	mr.FastForward(30 * time.Minute)
	assert.Equal(t, []string{"long"}, lookup(t, store.SessionIDsBySubject, "u1"))
}

func TestSessionIndex_WorksWithEncryption(t *testing.T) {
	mr := miniredis.RunT(t)
	store := newEncryptedStore(t, mr, redisoauth.EncryptionKey{ID: "k1", Key: key(1)})
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{UserID: "u1", SID: "sid-a", AccessToken: secret.NewFromString("a")}, time.Hour))
	assert.Equal(t, []string{"s1"}, lookup(t, store.SessionIDsBySID, "sid-a"))
	require.NoError(t, store.Delete(ctx, "s1"))
	assert.Empty(t, lookup(t, store.SessionIDsBySubject, "u1"))
	for _, k := range mr.Keys() {
		assert.False(t, strings.Contains(k, "subject:") || strings.Contains(k, "sid:"), "emptied index sets are removed: %s", k)
	}
}

func TestSessionWire_PersistsLifecycleFields(t *testing.T) {
	_, store := newStore(t)
	ctx := context.Background()
	created := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	active := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{
		UserID: "u1", SID: "sid-a", CreatedAt: created, LastActivity: active, AuthTime: created, AMR: []string{"pwd", "otp"},
	}, time.Hour))
	got, err := store.Get(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, []string{"pwd", "otp"}, got.AMR)
	assert.True(t, created.Equal(got.AuthTime))
	assert.Equal(t, "sid-a", got.SID)
	assert.True(t, created.Equal(got.CreatedAt))
	assert.True(t, active.Equal(got.LastActivity))
}

func TestReplayCache_SetNXWindow(t *testing.T) {
	mr, store := newStore(t)
	ctx := context.Background()
	first, err := store.Remember(ctx, "bcl:abc", time.Minute)
	require.NoError(t, err)
	assert.True(t, first)

	var wg sync.WaitGroup
	var mu sync.Mutex
	firsts := 0
	for range 8 {
		wg.Go(func() {
			ok, err := store.Remember(ctx, "bcl:race", time.Minute)
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				firsts++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, firsts, "exactly one concurrent caller records a key")

	first, err = store.Remember(ctx, "bcl:abc", time.Minute)
	require.NoError(t, err)
	assert.False(t, first)
	mr.FastForward(2 * time.Minute)
	first, err = store.Remember(ctx, "bcl:abc", time.Minute)
	require.NoError(t, err)
	assert.True(t, first)
}

func TestDeleteSessionsBySubject_RedisStore(t *testing.T) {
	_, store := newStore(t)
	issuer := newTokenIssuer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unused", http.StatusBadRequest) })
	var revoked []string
	client := newReplica(t, issuer, store, oauth2.WithBackChannelLogout(),
		oauth2.WithSessionRevocationHook(func(_ context.Context, subject string) { revoked = append(revoked, subject) }))
	ctx := context.Background()
	require.NoError(t, store.Put(ctx, "s1", oauth2.Session{UserID: "u1"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s2", oauth2.Session{UserID: "u1", SID: "sid-a"}, time.Hour))
	require.NoError(t, store.Put(ctx, "s3", oauth2.Session{UserID: "u2"}, time.Hour))

	n, err := client.DeleteSessionsBySubject(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"u1"}, revoked)
	for _, id := range []string{"s1", "s2"} {
		_, err := store.Get(ctx, id)
		assert.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	}
	_, err = store.Get(ctx, "s3")
	require.NoError(t, err)
	assert.Empty(t, lookup(t, store.SessionIDsBySID, "sid-a"))
}

func TestStateEntry_PersistsMaxAge(t *testing.T) {
	_, store := newStore(t)
	ctx := context.Background()
	zero := 0
	require.NoError(t, store.States().Put(ctx, "st1", oauth2.StateEntry{Nonce: "n", MaxAge: &zero}, time.Minute))
	got, err := store.States().Get(ctx, "st1")
	require.NoError(t, err)
	require.NotNil(t, got.MaxAge)
	assert.Equal(t, 0, *got.MaxAge)

	require.NoError(t, store.States().Put(ctx, "st2", oauth2.StateEntry{Nonce: "n"}, time.Minute))
	got, err = store.States().Get(ctx, "st2")
	require.NoError(t, err)
	assert.Nil(t, got.MaxAge)
}
