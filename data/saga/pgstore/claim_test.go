package pgstore_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/saga/pgstore/v2"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

// TestListResumable_ClaimsExclusivelyAcrossStores is the regression pin
// for review-13: concurrent multi-replica Resume must not both receive
// the same in-flight saga. ListResumable claims with a lease so a second
// store instance sees an empty list while the claim is live.
func TestListResumable_ClaimsExclusivelyAcrossStores(t *testing.T) {
	// Both stores share the same fake driver store via the registered name.
	db1, err := sql.Open(driverName, "")
	require.NoError(t, err)
	defer db1.Close()
	db2, err := sql.Open(driverName, "")
	require.NoError(t, err)
	defer db2.Close()

	s1 := pgstore.New(db1)
	s2 := pgstore.New(db2)
	ctx := context.Background()

	// Seed a resumable instance via s1.
	inst := saga.Instance{
		ID:         "saga-claim-1",
		Definition: "demo",
		State:      saga.StateRunning,
		// Non-zero UpdatedAt selects the UPDATE path on subsequent Puts;
		// first write uses zero UpdatedAt → INSERT.
	}
	require.NoError(t, s1.Put(ctx, inst))

	// Make the row "stale" enough for olderThan filtering: fake clock
	// advances on writes; claim path with olderThan=0 lists all free rows.
	first, err := s1.ListResumable(ctx, 0)
	require.NoError(t, err)
	require.Len(t, first, 1, "first resumer must claim the in-flight saga")
	require.Equal(t, "saga-claim-1", first[0].ID)

	second, err := s2.ListResumable(ctx, 0)
	require.NoError(t, err)
	require.Empty(t, second, "second resumer must not re-claim a live lease (would double-execute)")
}
