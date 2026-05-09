package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bds421/rho-kit/data/v2/idempotency"
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

	// Acquire + Set with the minimum TTL the API accepts.
	token, _, _, err := store.TryLock(ctx, "expire-key", nil, time.Second)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("test")}
	if err := store.Set(ctx, "expire-key", token, cached, time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Force the row past expiry without sleeping — intervalSeconds rounds
	// sub-second TTLs up to 1s, so a millisecond sleep would not actually
	// age the row in PostgreSQL's clock.
	if _, err := db.ExecContext(ctx,
		`UPDATE idempotency_keys SET expires_at = now() - interval '1 minute' WHERE key = $1`,
		"expire-key",
	); err != nil {
		t.Fatalf("force-expire: %v", err)
	}

	deleted, err := store.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted, got %d", deleted)
	}
}

// FR-030 [HIGH]: Set/Get must round-trip an empty-body response.
// Pre-fix the IS NOT NULL filter on response_body excluded the row
// (pgx maps Go nil []byte to SQL NULL), so an HTTP 204 cached
// response was never replayed and the handler re-executed.
func TestPgStore_GetReplaysEmptyBodyResponse(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()
	fp := []byte("body-hash-204")

	token, _, ok, err := store.TryLock(ctx, "empty-key", fp, time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: err=%v ok=%v", err, ok)
	}

	cached := idempotency.CachedResponse{
		StatusCode: 204, // No Content — empty body is the whole point
		Headers:    map[string][]string{"X-Request-Id": {"abc"}},
		Body:       nil, // explicit: handler wrote nothing
	}
	if err := store.Set(ctx, "empty-key", token, cached, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, mismatch, err := store.Get(ctx, "empty-key", fp)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mismatch {
		t.Fatal("unexpected fingerprint mismatch on empty-body replay")
	}
	if got == nil {
		t.Fatal("expected cached response for empty-body 204, got nil (FR-030 regression)")
	}
	if got.StatusCode != 204 {
		t.Errorf("status = %d, want 204", got.StatusCode)
	}
	if len(got.Body) != 0 {
		t.Errorf("expected empty body, got %d bytes (%q)", len(got.Body), got.Body)
	}
	if got.Headers["X-Request-Id"][0] != "abc" {
		t.Errorf("expected headers to round-trip, got %v", got.Headers)
	}
}

// FR-030 [HIGH] companion: Unlock must NOT remove a row that has
// already been Set with an empty body. Pre-fix the
// `response_body IS NULL` predicate on Unlock would treat the
// successfully-cached 204 as "still locked" and DELETE it, freeing
// the slot so the next caller would re-run the handler.
func TestPgStore_UnlockDoesNotDeleteEmptyBodyCachedRow(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()
	fp := []byte("body-hash-204")

	token, _, _, err := store.TryLock(ctx, "empty-key", fp, time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	cached := idempotency.CachedResponse{StatusCode: 204, Body: nil}
	if err := store.Set(ctx, "empty-key", token, cached, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Now an Unlock for the same token (panic-cleanup path) — must NOT
	// destroy the cached row.
	if err := store.Unlock(ctx, "empty-key", token); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	got, _, err := store.Get(ctx, "empty-key", fp)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Unlock destroyed cached empty-body row (FR-030 regression)")
	}
	if got.StatusCode != 204 {
		t.Errorf("status = %d, want 204", got.StatusCode)
	}
}
