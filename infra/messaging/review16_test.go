package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestDrainBatch_ClearsDirectInFlightOnPublishPanic pins review-16:
// a panic from publishFn during drain must not leave directInFlight stuck
// true (which freezes the publisher into buffer-only mode).
func TestDrainBatch_ClearsDirectInFlightOnPublishPanic(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	pub := newTestBufferedPublisher(
		func(context.Context, string, string, Message) error {
			if calls.Add(1) == 1 {
				panic("drain publish boom")
			}
			return nil
		},
		func() bool { return true },
		WithMaxSize(10),
	)
	msg, err := NewMessage("evt", map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	pub.mu.Lock()
	pub.pending = append(pub.pending, pendingMessage{
		Exchange:   "ex",
		RoutingKey: "rk",
		Msg:        msg,
	})
	pub.mu.Unlock()

	func() {
		defer func() { _ = recover() }()
		pub.drainBatch(context.Background())
	}()

	pub.mu.Lock()
	stuck := pub.directInFlight
	pub.mu.Unlock()
	if stuck {
		t.Fatal("directInFlight stuck true after publishFn panic in drainBatch")
	}
}

// TestSaveLocked_InvalidatesJournalReadyOnFailure pins review-16:
// a failed snapshot save must force the next persist to rewrite a full
// snapshot rather than appending to a stale journal.
func TestSaveLocked_InvalidatesJournalReadyOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")

	pub := newTestBufferedPublisher(
		func(context.Context, string, string, Message) error {
			return errors.New("broker down")
		},
		func() bool { return false },
		withStateFileAbsoluteForTest(state),
		WithMaxSize(10),
	)

	msg, err := NewMessage("evt", map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), "ex", "rk", msg); err != nil {
		t.Fatal(err)
	}
	pub.mu.Lock()
	if !pub.journalReady {
		pub.mu.Unlock()
		t.Fatal("expected journalReady after successful persist")
	}
	// Break the path so the next save fails (replace with a directory).
	if err := os.Remove(state); err != nil {
		pub.mu.Unlock()
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		pub.mu.Unlock()
		t.Fatal(err)
	}
	err = pub.saveLocked()
	ready := pub.journalReady
	pub.mu.Unlock()
	if err == nil {
		t.Fatal("expected saveLocked error")
	}
	if ready {
		t.Fatal("journalReady must be false after saveLocked failure")
	}
}

// TestAppendPendingEntry_RejectsSymlinkDestination pins review-16 M-05:
// journal append must refuse a symlinked state file.
func TestAppendPendingEntry_RejectsSymlinkDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := appendPendingEntry(link, []byte(`{"exchange":"e"}`))
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
}
