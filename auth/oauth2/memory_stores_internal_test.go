package oauth2

import (
	"context"
	"testing"
	"time"
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
