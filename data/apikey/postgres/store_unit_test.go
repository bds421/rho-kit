package postgres_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	postgres "github.com/bds421/rho-kit/data/apikey/postgres/v2"
)

func TestNew_PanicsOnNilPool(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil pool")
		}
	}()
	_ = postgres.New(nil)
}

func TestMigrations_Embedded(t *testing.T) {
	entries, err := postgres.Migrations.ReadDir("migrations")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "at least one migration must be embedded")

	// Every embedded migration must carry both goose direction markers so
	// it applies and rolls back cleanly.
	for _, e := range entries {
		data, err := postgres.Migrations.ReadFile("migrations/" + e.Name())
		require.NoError(t, err)
		content := string(data)
		require.Contains(t, content, "+goose Up", e.Name())
		require.Contains(t, content, "+goose Down", e.Name())
	}
}

// TestMigrations_FSContract guards the embed path the migrate tooling
// relies on: the FS must expose a "migrations" subtree.
func TestMigrations_FSContract(t *testing.T) {
	_, err := fstest.MapFS{}.Open(".") // sanity: fstest available
	require.NoError(t, err)
	_, err = postgres.Migrations.Open("migrations")
	require.NoError(t, err)
}
