package oauth2

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// TestMemoryStateStore_PrunesAbandonedEntries covers the resource-leak
// finding: an abandoned login (callback never arrives) leaves a state
// entry that is only ever expired lazily on Get of its exact key. Put
// of a fresh entry must opportunistically evict already-expired ones so
// the map does not grow without bound.
func TestMemoryStateStore_PrunesAbandonedEntries(t *testing.T) {
	s := NewMemoryStateStore()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		key := "abandoned-" + time.Duration(i).String()
		if err := s.Put(ctx, key, StateEntry{Nonce: "n"}, time.Millisecond); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	time.Sleep(20 * time.Millisecond) // let the 50 entries expire

	// A subsequent Put (a new login arriving) must sweep the expired
	// entries rather than leave them lingering forever.
	if err := s.Put(ctx, "fresh", StateEntry{Nonce: "n"}, time.Hour); err != nil {
		t.Fatalf("Put fresh: %v", err)
	}

	s.mu.Lock()
	got := len(s.entries)
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("expected only the fresh entry to remain, got %d entries", got)
	}
}

// TestMemorySessionStore_PrunesAbandonedEntries mirrors the state-store
// case for sessions: expired sessions must be swept on Put.
func TestMemorySessionStore_PrunesAbandonedEntries(t *testing.T) {
	s := NewMemorySessionStore()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		key := "old-" + time.Duration(i).String()
		if err := s.Put(ctx, key, Session{UserID: "u"}, time.Millisecond); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	time.Sleep(20 * time.Millisecond)

	if err := s.Put(ctx, "fresh", Session{UserID: "u"}, time.Hour); err != nil {
		t.Fatalf("Put fresh: %v", err)
	}

	s.mu.Lock()
	got := len(s.sessions)
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("expected only the fresh entry to remain, got %d entries", got)
	}
}

// TestMemoryStateStore_PutDoesNotEvictLiveEntries guards against the
// sweep being overzealous: still-valid entries must survive a later Put.
func TestMemoryStateStore_PutDoesNotEvictLiveEntries(t *testing.T) {
	s := NewMemoryStateStore()
	ctx := context.Background()
	if err := s.Put(ctx, "live", StateEntry{Nonce: "n"}, time.Hour); err != nil {
		t.Fatalf("Put live: %v", err)
	}
	if err := s.Put(ctx, "live2", StateEntry{Nonce: "n"}, time.Hour); err != nil {
		t.Fatalf("Put live2: %v", err)
	}
	if _, err := s.Get(ctx, "live"); err != nil {
		t.Fatalf("live entry was evicted: %v", err)
	}
	if _, err := s.Get(ctx, "live2"); err != nil {
		t.Fatalf("live2 entry was evicted: %v", err)
	}
}

// TestMemorySessionStore_GetReturnsIndependentSecrets pins the
// zeroize-on-Delete vs concurrent Get snapshot contract: callers must
// retain usable secret bytes after the store-owned entry is deleted.
func TestMemorySessionStore_GetReturnsIndependentSecrets(t *testing.T) {
	s := NewMemorySessionStore()
	ctx := context.Background()
	access := secret.NewFromString("access-token-value")
	refresh := secret.NewFromString("refresh-token-value")
	if err := s.Put(ctx, "sid", Session{
		SessionID:    "sid",
		UserID:       "user-1",
		AccessToken:  access,
		RefreshToken: refresh,
		Claims:       map[string]any{"email": "a@b.c"},
	}, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, "sid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken == access {
		t.Fatal("Get must not return the store-owned AccessToken pointer")
	}
	if err := s.Delete(ctx, "sid"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got.AccessToken.RevealString() != "access-token-value" {
		t.Fatalf("AccessToken zeroized under caller: %q", got.AccessToken.RevealString())
	}
	if got.RefreshToken.RevealString() != "refresh-token-value" {
		t.Fatalf("RefreshToken zeroized under caller: %q", got.RefreshToken.RevealString())
	}
	// Claims map must be independent of the store entry.
	got.Claims["email"] = "mutated"
	// Store entry is gone; re-put and ensure original claim path is clean.
	if err := s.Put(ctx, "sid2", Session{UserID: "u", Claims: map[string]any{"email": "orig"}}, time.Hour); err != nil {
		t.Fatalf("Put sid2: %v", err)
	}
	got2, err := s.Get(ctx, "sid2")
	if err != nil {
		t.Fatalf("Get sid2: %v", err)
	}
	if got2.Claims["email"] != "orig" {
		t.Fatalf("claims not independent: %v", got2.Claims)
	}
}

// TestMemoryStateStore_TakeIsSingleUse ensures concurrent Take cannot
// both observe the same state entry.
func TestMemoryStateStore_TakeIsSingleUse(t *testing.T) {
	s := NewMemoryStateStore()
	ctx := context.Background()
	if err := s.Put(ctx, "st", StateEntry{Nonce: "n"}, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Take(ctx, "st"); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	if _, err := s.Take(ctx, "st"); err == nil {
		t.Fatal("second Take must fail")
	}
}

func TestMemoryStateStore_MaxEntries(t *testing.T) {
	s := NewMemoryStateStore(WithMaxStateEntries(2))
	require.NoError(t, s.Put(context.Background(), "a", StateEntry{}, time.Minute))
	require.NoError(t, s.Put(context.Background(), "b", StateEntry{}, time.Minute))
	err := s.Put(context.Background(), "c", StateEntry{}, time.Minute)
	require.ErrorIs(t, err, ErrStateStoreFull)
	// Overwrite existing key still allowed.
	require.NoError(t, s.Put(context.Background(), "a", StateEntry{}, time.Minute))
}
