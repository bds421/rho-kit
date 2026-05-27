package pgstore_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/cron/pgstore/v2"
)

func TestStore_NewPanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil db")
		}
	}()
	_ = pgstore.New(nil)
}

func TestWithTableName_PanicsOnInvalidIdentifier(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on bad identifier")
		}
	}()
	_ = pgstore.WithTableName("drop; --")
}

func TestIsValidName(t *testing.T) {
	cases := map[string]bool{
		"nightly-cleanup":          true,
		"job_a_42":                 true,
		"a":                        true,
		strings.Repeat("a", 128):   true,
		"":                         false,
		"BadCase":                  false,
		"with space":               false,
		"trailing-newline\n":       false,
		"semicolon;drop":           false,
		strings.Repeat("a", 129):   false,
		"1-leading-digit":          false,
	}
	for name, ok := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, ok, pgstore.IsValidName(name))
		})
	}
}

func TestIsValidIdent(t *testing.T) {
	cases := map[string]bool{
		"cron_schedules":        true,
		"schema.cron_schedules": true,
		"_underscore_prefix":    true,
		"drop; --":              false,
		"with space":            false,
		"a..b":                  false,
		"":                      false,
	}
	for ident, ok := range cases {
		t.Run(ident, func(t *testing.T) {
			require.Equal(t, ok, pgstore.IsValidIdent(ident))
		})
	}
}

func TestValidateRecord(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		err := pgstore.ValidateRecord(pgstore.ScheduleRecord{
			Name: "nightly-cleanup",
			Spec: "0 3 * * *",
		})
		require.NoError(t, err)
	})

	t.Run("empty name", func(t *testing.T) {
		err := pgstore.ValidateRecord(pgstore.ScheduleRecord{
			Name: "",
			Spec: "@hourly",
		})
		require.Error(t, err)
	})

	t.Run("empty spec", func(t *testing.T) {
		err := pgstore.ValidateRecord(pgstore.ScheduleRecord{
			Name: "ok",
			Spec: "",
		})
		require.Error(t, err)
	})

	t.Run("oversize spec", func(t *testing.T) {
		err := pgstore.ValidateRecord(pgstore.ScheduleRecord{
			Name: "ok",
			Spec: strings.Repeat("*", 129),
		})
		require.Error(t, err)
	})

	t.Run("uppercase name rejected", func(t *testing.T) {
		err := pgstore.ValidateRecord(pgstore.ScheduleRecord{
			Name: "BadCase",
			Spec: "@hourly",
		})
		require.Error(t, err)
	})
}

// SQL-roundtrip tests (Add / Upsert / Remove / Enable / Get / List /
// ApplyTo) belong under //go:build integration with infra/sqldb/dbtest.
// They are intentionally not included in the unit tier to keep
// `go test ./...` Docker-free.
