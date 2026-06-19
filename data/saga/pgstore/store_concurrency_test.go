package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bds421/rho-kit/data/saga/pgstore/v2"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

// TestPut_SequentialSameProcessAdvances reproduces how
// saga.DurableExecutor.executeInstance drives a multi-step saga: it Gets
// the instance ONCE, then calls Put repeatedly with the in-memory
// Instance whose UpdatedAt stays fixed at the value read by that single
// Get. The server stamps a fresh updated_at on every write, so the
// caller's snapshot goes stale after the first Put. The store must still
// accept the caller's subsequent same-process Puts rather than failing
// them as concurrent conflicts.
func TestPut_SequentialSameProcessAdvances(t *testing.T) {
	db := openFakeDB(t)
	defer func() { _ = db.Close() }()
	store := pgstore.New(db)
	ctx := context.Background()

	// Start: first write of a fresh instance (UpdatedAt zero -> INSERT).
	const id = "saga-1"
	if err := store.Put(ctx, saga.Instance{ID: id, Definition: "d", State: saga.StatePending}); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	// executeInstance Gets ONCE and mutates this snapshot in place.
	inst, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Put #1: persist running state.
	inst.State = saga.StateRunning
	if err := store.Put(ctx, inst); err != nil {
		t.Fatalf("Put running: %v", err)
	}

	// Put #2: advance through step 0 (executor does NOT re-Get).
	inst.CurrentStep = 1
	if err := store.Put(ctx, inst); err != nil {
		if errors.Is(err, pgstore.ErrConcurrentUpdate) {
			t.Fatalf("Put step advance #1 spuriously reported a concurrent update; the executor never re-Gets, so this fails every multi-step saga")
		}
		t.Fatalf("Put step advance #1: %v", err)
	}

	// Put #3: advance through step 1.
	inst.CurrentStep = 2
	if err := store.Put(ctx, inst); err != nil {
		if errors.Is(err, pgstore.ErrConcurrentUpdate) {
			t.Fatalf("Put step advance #2 spuriously reported a concurrent update")
		}
		t.Fatalf("Put step advance #2: %v", err)
	}

	// Put #4: mark completed.
	inst.State = saga.StateCompleted
	if err := store.Put(ctx, inst); err != nil {
		t.Fatalf("Put completed: %v", err)
	}

	// Final state must reflect the last write.
	final, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if final.State != saga.StateCompleted {
		t.Fatalf("final state = %q, want completed", final.State)
	}
	if final.CurrentStep != 2 {
		t.Fatalf("final current_step = %d, want 2", final.CurrentStep)
	}
}

// TestPut_FirstWriteCollision keeps the genuine first-write guard: an
// INSERT whose row already exists (a sibling replica created it first)
// must still surface ErrConcurrentUpdate, since the insert path never
// overwrites.
func TestPut_FirstWriteCollision(t *testing.T) {
	db := openFakeDB(t)
	defer func() { _ = db.Close() }()
	store := pgstore.New(db)
	ctx := context.Background()

	const id = "saga-collision"
	if err := store.Put(ctx, saga.Instance{ID: id, Definition: "d", State: saga.StatePending}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Second fresh-instance Put (UpdatedAt zero) for the same ID must not
	// overwrite; it must report the conflict.
	err := store.Put(ctx, saga.Instance{ID: id, Definition: "d", State: saga.StatePending})
	if !errors.Is(err, pgstore.ErrConcurrentUpdate) {
		t.Fatalf("insert collision: got err=%v, want ErrConcurrentUpdate", err)
	}
}

// TestPut_UpdateVanishedRow keeps the genuine update guard: if the row
// is gone by the time an UpdatedAt-bearing Put runs (e.g. a concurrent
// Delete), the zero-row update must surface ErrConcurrentUpdate rather
// than silently succeeding.
func TestPut_UpdateVanishedRow(t *testing.T) {
	db := openFakeDB(t)
	defer func() { _ = db.Close() }()
	store := pgstore.New(db)
	ctx := context.Background()

	const id = "saga-vanished"
	if err := store.Put(ctx, saga.Instance{ID: id, Definition: "d", State: saga.StatePending}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	inst, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	inst.State = saga.StateRunning
	if err := store.Put(ctx, inst); !errors.Is(err, pgstore.ErrConcurrentUpdate) {
		t.Fatalf("update vanished row: got err=%v, want ErrConcurrentUpdate", err)
	}
}
