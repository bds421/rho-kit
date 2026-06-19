package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bds421/rho-kit/data/saga/pgstore/v2"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

func TestNew_PanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = pgstore.New(nil)
}

func TestWithTableName_PanicsOnUnsafeIdentifier(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = pgstore.WithTableName("drop;")
}

// TestZeroStore_MethodsReturnErrInvalidStore pins the kit-wide
// invalid-receiver convention (mirrors queue.ErrInvalidQueue /
// ratelimit.ErrInvalidLimiter): a zero-value &Store{} that bypassed New
// (so db is nil) must return ErrInvalidStore from every StateStore
// method rather than panicking with a nil-pointer dereference.
func TestZeroStore_MethodsReturnErrInvalidStore(t *testing.T) {
	var s pgstore.Store // zero value: db nil, table empty
	ctx := context.Background()

	if err := s.Put(ctx, saga.Instance{ID: "x"}); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Put: got err=%v, want ErrInvalidStore", err)
	}
	if _, err := s.Get(ctx, "x"); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Get: got err=%v, want ErrInvalidStore", err)
	}
	if _, err := s.ListResumable(ctx, time.Minute); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("ListResumable: got err=%v, want ErrInvalidStore", err)
	}
	if err := s.Delete(ctx, "x"); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Delete: got err=%v, want ErrInvalidStore", err)
	}
}

// TestNilStore_MethodsReturnErrInvalidStore guards the nil-receiver case
// for the pointer methods: a *Store typed nil must also yield
// ErrInvalidStore (ready() checks s == nil before any field access).
func TestNilStore_MethodsReturnErrInvalidStore(t *testing.T) {
	var s *pgstore.Store // typed nil
	ctx := context.Background()

	if err := s.Put(ctx, saga.Instance{ID: "x"}); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Put: got err=%v, want ErrInvalidStore", err)
	}
	if _, err := s.Get(ctx, "x"); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Get: got err=%v, want ErrInvalidStore", err)
	}
	if _, err := s.ListResumable(ctx, 0); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("ListResumable: got err=%v, want ErrInvalidStore", err)
	}
	if err := s.Delete(ctx, "x"); !errors.Is(err, pgstore.ErrInvalidStore) {
		t.Fatalf("Delete: got err=%v, want ErrInvalidStore", err)
	}
}

// Full SQL-roundtrip tests (Put / Get / ListResumable / Delete /
// optimistic-concurrency) belong under //go:build integration with
// infra/sqldb/dbtest. Unit tier exercises the panic guards above.
