package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/data/v2/idempotency"
)

func TestNew_PanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	New(nil)
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	New(&sql.DB{}, nil)
}

func TestNew_InvalidTableNamePanicDoesNotReflectName(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic, got none")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("panic must be a stable string, got %T", rec)
		}
		if msg != "pgstore: invalid table name" {
			t.Fatalf("panic = %q, want %q", msg, "pgstore: invalid table name")
		}
		if strings.Contains(msg, "secret_token") {
			t.Fatalf("panic reflected table name: %q", msg)
		}
	}()

	New(&sql.DB{}, WithTableName("secret_token;drop"))
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*Store{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			resp, mismatch, err := store.Get(ctx, "k", nil)
			if resp != nil || mismatch || !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Get = resp=%v mismatch=%v err=%v, want ErrInvalidStore", resp, mismatch, err)
			}

			token, mismatch, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
			if token != "" || mismatch || ok || !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("TryLock = token=%q mismatch=%v ok=%v err=%v, want ErrInvalidStore", token, mismatch, ok, err)
			}

			if err := store.Set(ctx, "k", "t", idempotency.CachedResponse{}, time.Minute); !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Set = %v, want ErrInvalidStore", err)
			}

			if err := store.Unlock(ctx, "k", "t"); !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Unlock = %v, want ErrInvalidStore", err)
			}

			if _, err := store.DeleteExpired(ctx); !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("DeleteExpired = %v, want ErrInvalidStore", err)
			}
		})
	}
}

func TestStore_RejectsInvalidKeyBeforeDBUse(t *testing.T) {
	store := &Store{db: &sql.DB{}, table: "idempotency_keys"}
	ctx := context.Background()

	resp, mismatch, err := store.Get(ctx, "", nil)
	if resp != nil || mismatch || !errors.Is(err, idempotency.ErrKeyEmpty) {
		t.Fatalf("Get empty key = resp=%v mismatch=%v err=%v, want ErrKeyEmpty", resp, mismatch, err)
	}

	token, mismatch, ok, err := store.TryLock(ctx, "", nil, time.Minute)
	if token != "" || mismatch || ok || !errors.Is(err, idempotency.ErrKeyEmpty) {
		t.Fatalf("TryLock empty key = token=%q mismatch=%v ok=%v err=%v, want ErrKeyEmpty", token, mismatch, ok, err)
	}

	if err := store.Set(ctx, "", "t", idempotency.CachedResponse{}, time.Minute); !errors.Is(err, idempotency.ErrKeyEmpty) {
		t.Fatalf("Set empty key = %v, want ErrKeyEmpty", err)
	}

	if err := store.Unlock(ctx, "", "t"); !errors.Is(err, idempotency.ErrKeyEmpty) {
		t.Fatalf("Unlock empty key = %v, want ErrKeyEmpty", err)
	}
}

func TestStore_SetRejectsInvalidCachedResponseBeforeDBUse(t *testing.T) {
	store := &Store{db: &sql.DB{}, table: "idempotency_keys"}

	err := store.Set(context.Background(), "k", "token", idempotency.CachedResponse{StatusCode: 99}, time.Minute)
	if !errors.Is(err, idempotency.ErrInvalidCachedResponse) {
		t.Fatalf("Set invalid response = %v, want ErrInvalidCachedResponse", err)
	}
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
