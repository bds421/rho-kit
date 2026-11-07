package auditlog

import (
	"context"
	"errors"
	"testing"
)

// A refusal joins no transaction; once the joined one holds the chain lock,
// a refusal from its ctx fails at once with ErrChainHeld, and a success
// event still goes through.
func TestJoinedRefusalsBeforeAndAfterTheChainLock(t *testing.T) {
	store := NewMemoryStore()
	logger := newTestLogger(store)
	ctx := Join(context.Background(), "tx")
	refusal := Event{Actor: "a", Action: "x", Resource: "r", Status: StatusDenied}

	if err := logger.LogE(ctx, refusal); err != nil {
		t.Fatalf("a refusal before the lock: %v", err)
	}
	MarkChainHeld(ctx)
	if err := logger.LogE(ctx, refusal); !errors.Is(err, ErrChainHeld) {
		t.Fatalf("a refusal after the lock: %v, want ErrChainHeld", err)
	}
	if err := logger.LogE(ctx, Event{Actor: "a", Action: "x", Resource: "r", Status: StatusSuccess}); err != nil {
		t.Fatalf("a success after the lock: %v", err)
	}
	if tx, ok := Joined(ctx); !ok || tx != "tx" {
		t.Fatalf("Joined = %v, %v", tx, ok)
	}
}
