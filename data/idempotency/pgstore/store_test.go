package pgstore

import (
	"context"
	"database/sql"
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

	// Create the table for testing.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS idempotency_keys (
			key           VARCHAR(512) PRIMARY KEY,
			status_code   INT,
			headers       JSONB,
			response_body BYTEA,
			expires_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Clean up before each test.
	_, _ = db.Exec("DELETE FROM idempotency_keys")

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPgStore_GetMiss(t *testing.T) {
	db := testDB(t)
	store := New(db)

	resp, err := store.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response for missing key")
	}
}

func TestPgStore_SetAndGet(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	cached := idempotency.CachedResponse{
		StatusCode: 200,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"ok":true}`),
	}

	if err := store.Set(ctx, "test-key", cached, 5*time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx, "test-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected cached response, got nil")
	}
	if got.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", got.StatusCode)
	}
	if string(got.Body) != `{"ok":true}` {
		t.Errorf("Body = %q, want %q", got.Body, `{"ok":true}`)
	}
}

func TestPgStore_TryLock(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	acquired, err := store.TryLock(ctx, "lock-key", time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !acquired {
		t.Fatal("expected lock to be acquired")
	}

	// Second lock attempt should fail.
	acquired2, err := store.TryLock(ctx, "lock-key", time.Minute)
	if err != nil {
		t.Fatalf("TryLock second: %v", err)
	}
	if acquired2 {
		t.Fatal("expected second lock to fail")
	}
}

func TestPgStore_Unlock(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	_, _ = store.TryLock(ctx, "unlock-key", time.Minute)

	if err := store.Unlock(ctx, "unlock-key"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// After unlock, should be able to lock again.
	acquired, err := store.TryLock(ctx, "unlock-key", time.Minute)
	if err != nil {
		t.Fatalf("TryLock after unlock: %v", err)
	}
	if !acquired {
		t.Fatal("expected lock after unlock")
	}
}

func TestPgStore_DeleteExpired(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	// Insert with very short TTL.
	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("test")}
	if err := store.Set(ctx, "expire-key", cached, time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	deleted, err := store.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted, got %d", deleted)
	}
}

func TestPgStore_UnlockDoesNotDeleteCachedResponse(t *testing.T) {
	db := testDB(t)
	store := New(db)
	ctx := context.Background()

	// Lock, then set response, then unlock — response should still be there.
	_, _ = store.TryLock(ctx, "keep-key", time.Minute)

	cached := idempotency.CachedResponse{StatusCode: 201, Body: []byte("created")}
	if err := store.Set(ctx, "keep-key", cached, 5*time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Unlock should be a no-op because response_body is now set.
	if err := store.Unlock(ctx, "keep-key"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	got, err := store.Get(ctx, "keep-key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected cached response to survive unlock")
	}
}
