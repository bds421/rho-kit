package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bds421/rho-kit/data/idempotency"
)

// testDB returns a test database connection, or skips the test if unavailable.
// For CI, use testcontainers; for local dev, use an existing PostgreSQL instance.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "postgres://postgres:postgres@localhost:5432/kit_test?sslmode=disable"
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping pgstore test: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("skipping pgstore test (no database): %v", err)
	}

	// Create the table for testing — schema mirrors the production migration
	// including owner_token + fingerprint columns.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS idempotency_keys (
			key           VARCHAR(512) PRIMARY KEY,
			status_code   INT,
			headers       JSONB,
			response_body BYTEA,
			expires_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			owner_token   VARCHAR(64),
			fingerprint   BYTEA
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Clean up before each test.
	_, _ = db.Exec("DELETE FROM idempotency_keys")

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNew_PanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	New(nil)
}

// TestIntervalSeconds_RoundsSubSecondUp guards the TTL precision fix:
// PostgreSQL intervals here use second precision, but truncating sub-second
// durations with int(d.Seconds()) used to round 500ms to "0 seconds" — the
// row would expire before any caller could observe it.
func TestIntervalSeconds_RoundsSubSecondUp(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{1 * time.Nanosecond, "1 seconds"},
		{500 * time.Millisecond, "1 seconds"},
		{999 * time.Millisecond, "1 seconds"},
		{1 * time.Second, "1 seconds"},
		{1500 * time.Millisecond, "2 seconds"},
		{60 * time.Second, "60 seconds"},
		{24 * time.Hour, "86400 seconds"},
	}
	for _, c := range cases {
		if got := intervalSeconds(c.in); got != c.want {
			t.Errorf("intervalSeconds(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPgStore_GetMiss(t *testing.T) {
	db := testDB(t)
	store := New(db)

	resp, fpMismatch, err := store.Get(context.Background(), "nonexistent", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response for missing key")
	}
	if fpMismatch {
		t.Fatal("missing key should not report fingerprint mismatch")
	}
}

func TestPgStore_TryLockSetAndGet(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()
	fp := []byte("body-hash-1")

	token, mismatch, ok, err := store.TryLock(ctx, "test-key", fp, 5*time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok || mismatch || token == "" {
		t.Fatalf("expected acquired lock; got token=%q mismatch=%v ok=%v", token, mismatch, ok)
	}

	cached := idempotency.CachedResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"ok":true}`),
	}
	if err := store.Set(ctx, "test-key", token, cached, 5*time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, mismatch, err := store.Get(ctx, "test-key", fp)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mismatch {
		t.Fatal("expected fingerprint match")
	}
	if got == nil || got.StatusCode != 200 || string(got.Body) != `{"ok":true}` {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestPgStore_GetWithDifferentFingerprint(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	// Acquire + Set with fingerprint A.
	token, _, ok, err := store.TryLock(ctx, "k", []byte("A"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}
	cached := idempotency.CachedResponse{StatusCode: 201, Body: []byte("created")}
	if err := store.Set(ctx, "k", token, cached, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get with fingerprint B → mismatch.
	resp, mismatch, err := store.Get(ctx, "k", []byte("B"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !mismatch {
		t.Fatal("expected fingerprint mismatch")
	}
	if resp != nil {
		t.Fatal("expected nil response on mismatch")
	}
}

func TestPgStore_TryLockFingerprintMismatchOnContended(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	_, _, ok, err := store.TryLock(ctx, "k", []byte("A"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok=%v err=%v", ok, err)
	}

	// Second TryLock with different fingerprint → mismatch (not just contended).
	_, mismatch, ok, err := store.TryLock(ctx, "k", []byte("B"), time.Minute)
	if err != nil {
		t.Fatalf("second TryLock: %v", err)
	}
	if ok {
		t.Fatal("expected lock acquisition to fail")
	}
	if !mismatch {
		t.Fatal("expected fingerprint mismatch on contended different-body retry")
	}
}

func TestPgStore_TryLockSameFingerprintIsContended(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()
	fp := []byte("same")

	_, _, ok, err := store.TryLock(ctx, "k", fp, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok=%v err=%v", ok, err)
	}

	// Second TryLock with same fingerprint → contended (409), not mismatch.
	_, mismatch, ok, err := store.TryLock(ctx, "k", fp, time.Minute)
	if err != nil {
		t.Fatalf("second TryLock: %v", err)
	}
	if ok {
		t.Fatal("expected lock to be contended")
	}
	if mismatch {
		t.Fatal("same fingerprint should not report mismatch")
	}
}

func TestPgStore_SetReturnsErrLockLostOnTokenMismatch(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	_, _, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}

	// A different token (e.g. from a stale handler whose TTL expired and
	// another caller has since acquired) must not be able to write.
	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("x")}
	err = store.Set(ctx, "k", "wrong-token", cached, time.Minute)
	if !errors.Is(err, idempotency.ErrLockLost) {
		t.Fatalf("expected ErrLockLost, got %v", err)
	}
}

func TestPgStore_UnlockTokenScoped(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	token, _, _, err := store.TryLock(ctx, "k", nil, time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}

	// Unlock with wrong token is a no-op (still locked).
	if err := store.Unlock(ctx, "k", "wrong-token"); err != nil {
		t.Fatalf("Unlock with wrong token: %v", err)
	}
	_, _, ok, _ := store.TryLock(ctx, "k", nil, time.Minute)
	if ok {
		t.Fatal("lock should still be held after wrong-token Unlock")
	}

	// Unlock with correct token succeeds and frees the slot.
	if err := store.Unlock(ctx, "k", token); err != nil {
		t.Fatalf("Unlock with correct token: %v", err)
	}
	_, _, ok, _ = store.TryLock(ctx, "k", nil, time.Minute)
	if !ok {
		t.Fatal("lock should be reacquirable after correct Unlock")
	}
}

func TestPgStore_DeleteExpired(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	// Acquire + Set with very short TTL.
	token, _, _, err := store.TryLock(ctx, "expire-key", nil, time.Millisecond)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("test")}
	// Set will fail because the lock TTL is already expired by the time we get here;
	// in that case the row is already in a "done" state (just locked-then-expired).
	_ = store.Set(ctx, "expire-key", token, cached, time.Millisecond)

	time.Sleep(10 * time.Millisecond)

	deleted, err := store.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted, got %d", deleted)
	}
}
