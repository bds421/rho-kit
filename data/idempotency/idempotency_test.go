package idempotency

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStore_TryLock_RejectsZeroTTL(t *testing.T) {
	store := NewMemoryStore()
	_, _, _, err := store.TryLock(context.Background(), "k", []byte("fp"), 0)
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=0: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_TryLock_RejectsNegativeTTL(t *testing.T) {
	store := NewMemoryStore()
	_, _, _, err := store.TryLock(context.Background(), "k", []byte("fp"), -1*time.Second)
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=-1s: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_Set_RejectsNonPositiveTTL(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	token, _, ok, err := store.TryLock(ctx, "k", []byte("fp"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("setup: TryLock failed: ok=%v err=%v", ok, err)
	}

	resp := CachedResponse{StatusCode: 200, Body: []byte("ok")}

	if err := store.Set(ctx, "k", token, resp, 0); !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=0: got %v, want ErrInvalidTTL", err)
	}
	if err := store.Set(ctx, "k", token, resp, -time.Second); !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=-1s: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_TryLock_PositiveTTLSucceeds(t *testing.T) {
	// Sanity check that the guard didn't break the happy path.
	store := NewMemoryStore()
	token, mismatch, ok, err := store.TryLock(context.Background(), "k", []byte("fp"), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mismatch || !ok || token == "" {
		t.Errorf("got token=%q mismatch=%v ok=%v; want acquired", token, mismatch, ok)
	}
}

func TestWithMemoryStoreClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithMemoryStoreClock(nil)
}

// TestMemoryStore_SubSecondTTL_NotImmediatelyExpired guards the contract
// documented at the package level: a positive TTL — even sub-second — MUST
// produce a row that's reachable for at least a moment. MemoryStore stores
// nanosecond expiry so this is the easy case; the pgstore variant of the
// same property lives next to its second-precision rounding helper.
func TestMemoryStore_SubSecondTTL_NotImmediatelyExpired(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	token, _, ok, err := store.TryLock(ctx, "k", []byte("fp"), 500*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("TryLock(500ms): ok=%v err=%v", ok, err)
	}
	resp := CachedResponse{StatusCode: 200, Body: []byte("ok")}
	if err := store.Set(ctx, "k", token, resp, 500*time.Millisecond); err != nil {
		t.Fatalf("Set(500ms): %v", err)
	}
	got, _, err := store.Get(ctx, "k", []byte("fp"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("entry expired immediately for 500ms TTL")
	}
}
