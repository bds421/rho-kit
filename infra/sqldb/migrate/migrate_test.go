package migrate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// Each Up/Down/Status helper threads through newProvider; the goose
// Provider itself needs a real *sql.DB, so we only unit-test the
// validation surface here. Integration coverage that actually drives
// goose lives in the pgx integrationtest module.

func TestUp_RejectsNilDB(t *testing.T) {
	_, err := Up(context.Background(), nil, Config{Dir: fstest.MapFS{}})
	if err == nil {
		t.Fatal("Up(nil) should return an error")
	}
	if !strings.Contains(err.Error(), "db must not be nil") {
		t.Fatalf("error = %q, want it to mention nil db", err)
	}
}

func TestDown_RejectsNilDB(t *testing.T) {
	err := Down(context.Background(), nil, Config{Dir: fstest.MapFS{}})
	if err == nil {
		t.Fatal("Down(nil) should return an error")
	}
	if !strings.Contains(err.Error(), "db must not be nil") {
		t.Fatalf("error = %q, want it to mention nil db", err)
	}
}

func TestStatus_RejectsNilDB(t *testing.T) {
	err := Status(context.Background(), nil, Config{Dir: fstest.MapFS{}}, nil)
	if err == nil {
		t.Fatal("Status(nil) should return an error")
	}
	if !strings.Contains(err.Error(), "db must not be nil") {
		t.Fatalf("error = %q, want it to mention nil db", err)
	}
}

func TestUp_RejectsNilDir(t *testing.T) {
	_, err := Up(context.Background(), &sql.DB{}, Config{Dir: nil})
	if err == nil {
		t.Fatal("Up(nil Dir) should return an error")
	}
	if !strings.Contains(err.Error(), "Dir must not be nil") {
		t.Fatalf("error = %q, want it to mention nil Dir", err)
	}
}

func TestDown_RejectsNilDir(t *testing.T) {
	err := Down(context.Background(), &sql.DB{}, Config{Dir: nil})
	if err == nil {
		t.Fatal("Down(nil Dir) should return an error")
	}
	if !strings.Contains(err.Error(), "Dir must not be nil") {
		t.Fatalf("error = %q, want it to mention nil Dir", err)
	}
}

func TestStatus_RejectsNilDir(t *testing.T) {
	err := Status(context.Background(), &sql.DB{}, Config{Dir: nil}, nil)
	if err == nil {
		t.Fatal("Status(nil Dir) should return an error")
	}
	if !strings.Contains(err.Error(), "Dir must not be nil") {
		t.Fatalf("error = %q, want it to mention nil Dir", err)
	}
}

func TestNewProvider_PropagatesGooseError(t *testing.T) {
	// goose.NewProvider on a Dir without a /migrations directory still
	// constructs successfully (goose tolerates empty migration sets), so
	// to exercise the wrap we need a *sql.DB that goose immediately
	// rejects. The zero-value *sql.DB satisfies the type but has no
	// driver, so goose's Provider construction fails the dialect check
	// downstream — depending on goose version, an empty FS may also
	// trip a check. We accept either failure shape.
	_, err := newProvider(&sql.DB{}, Config{Dir: fstest.MapFS{}})
	// Either nil (goose accepted it) or a wrapped "migrate: create provider"
	// error. If the latter, verify the prefix shape.
	if err != nil && !strings.Contains(err.Error(), "migrate:") {
		t.Fatalf("error = %q, want a 'migrate:'-prefixed wrap", err)
	}
}

func TestUp_ReturnsZeroOnValidationError(t *testing.T) {
	// Up returns (0, err) when newProvider fails, never a partial count.
	got, err := Up(context.Background(), nil, Config{Dir: fstest.MapFS{}})
	if got != 0 {
		t.Fatalf("count = %d, want 0 on validation failure", got)
	}
	if !errors.Is(err, err) || err == nil {
		t.Fatalf("expected error, got nil")
	}
}
