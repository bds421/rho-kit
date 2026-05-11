//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bds421/rho-kit/data/idempotency/pgstore/v2"
	"github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()

	cfg := dbtest.StartPostgres(t, "kit_test")
	db, err := sql.Open("pgx", postgresDSN(cfg))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	waitForPostgres(t, db)

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

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func waitForPostgres(t *testing.T, db *sql.DB) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("ping postgres: %v", lastErr)
}

func postgresDSN(cfg sqldb.Config) string {
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func TestPgStore_GetMiss(t *testing.T) {
	db := testDB(t)
	store := pgstore.New(db)

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
	store := pgstore.New(db)
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
	store := pgstore.New(db)
	ctx := context.Background()

	token, _, ok, err := store.TryLock(ctx, "k", []byte("A"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}
	cached := idempotency.CachedResponse{StatusCode: 201, Body: []byte("created")}
	if err := store.Set(ctx, "k", token, cached, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

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
	store := pgstore.New(db)
	ctx := context.Background()

	_, _, ok, err := store.TryLock(ctx, "k", []byte("A"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok=%v err=%v", ok, err)
	}

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
	store := pgstore.New(db)
	ctx := context.Background()
	fp := []byte("same")

	_, _, ok, err := store.TryLock(ctx, "k", fp, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok=%v err=%v", ok, err)
	}

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
	store := pgstore.New(db)
	ctx := context.Background()

	_, _, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}

	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("x")}
	err = store.Set(ctx, "k", "wrong-token", cached, time.Minute)
	if !errors.Is(err, idempotency.ErrLockLost) {
		t.Fatalf("expected ErrLockLost, got %v", err)
	}
}

func TestPgStore_UnlockTokenScoped(t *testing.T) {
	db := testDB(t)
	store := pgstore.New(db)
	ctx := context.Background()

	token, _, _, err := store.TryLock(ctx, "k", nil, time.Minute)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}

	if err := store.Unlock(ctx, "k", "wrong-token"); err != nil {
		t.Fatalf("Unlock with wrong token: %v", err)
	}
	_, _, ok, _ := store.TryLock(ctx, "k", nil, time.Minute)
	if ok {
		t.Fatal("lock should still be held after wrong-token Unlock")
	}

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
	store := pgstore.New(db)
	ctx := context.Background()

	token, _, _, err := store.TryLock(ctx, "expire-key", nil, time.Second)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	cached := idempotency.CachedResponse{StatusCode: 200, Body: []byte("test")}
	if err := store.Set(ctx, "expire-key", token, cached, time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
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

func TestPgStore_GetReplaysEmptyBodyResponse(t *testing.T) {
	db := testDB(t)
	store := pgstore.New(db)
	ctx := context.Background()
	fp := []byte("body-hash-204")

	token, _, ok, err := store.TryLock(ctx, "empty-key", fp, time.Minute)
	if err != nil || !ok {
		t.Fatalf("TryLock: err=%v ok=%v", err, ok)
	}

	cached := idempotency.CachedResponse{
		StatusCode: 204,
		Headers:    map[string][]string{"X-Request-Id": {"abc"}},
		Body:       nil,
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
		t.Fatal("expected cached response for empty-body 204, got nil")
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

func TestPgStore_UnlockDoesNotDeleteEmptyBodyCachedRow(t *testing.T) {
	db := testDB(t)
	store := pgstore.New(db)
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
	if err := store.Unlock(ctx, "empty-key", token); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	got, _, err := store.Get(ctx, "empty-key", fp)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Unlock destroyed cached empty-body row")
	}
	if got.StatusCode != 204 {
		t.Errorf("status = %d, want 204", got.StatusCode)
	}
}
