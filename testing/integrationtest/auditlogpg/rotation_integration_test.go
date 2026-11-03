//go:build integration

package auditlogpg

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
)

// TestPostgres_RotationMarkerAndAnchor rotates from the unkeyed chain key to
// a key list over a real store: one marker per rotation, verification
// across it, and the anchor catching a tail rewritten with the old key.
func TestPostgres_RotationMarkerAndAnchor(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	store := postgresstore.New(pool)
	event := auditlog.Event{Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess}

	legacy := newLogger(t, pool)
	require.NoError(t, legacy.LogE(ctx, event))

	old := auditlog.ChainKey{ID: "1", Key: testChainKey}
	next := auditlog.ChainKey{ID: "2", Key: bytes.Repeat([]byte("n"), auditlog.MinChainKeyLen)}
	rotated := func() *auditlog.Logger {
		return auditlog.New(store, auditlog.WithChainKeys(next, old), auditlog.WithCursorKey(testCursorKey))
	}
	require.NoError(t, rotated().RecordRotation(ctx))
	require.NoError(t, rotated().LogE(ctx, event))

	head, found, err := store.Head(ctx)
	require.NoError(t, err)
	require.True(t, found)
	anchor, err := rotated().Verify(ctx, auditlog.Verification{})
	require.NoError(t, err)
	assert.Equal(t, head.HMAC, anchor.HMAC)
	assert.Equal(t, 3, anchor.Events, "the legacy event, one marker, the new event")

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	_, err = sqlDB.Exec("DELETE FROM audit_log_events WHERE seq > (SELECT MIN(seq) FROM audit_log_events)")
	require.NoError(t, err)
	require.NoError(t, legacy.LogE(ctx, event))

	_, err = rotated().Verify(ctx, auditlog.Verification{Anchor: anchor})
	assert.ErrorIs(t, err, auditlog.ErrChainBroken)
}
