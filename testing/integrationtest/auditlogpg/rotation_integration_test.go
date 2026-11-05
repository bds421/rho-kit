//go:build integration

package auditlogpg

import (
	"bytes"
	"context"
	"testing"
	"time"

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

// TestPostgres_RollingRotationTwoWriters interleaves a replica still sealing
// with the old key and one on the new key: the old one follows the head once
// it knows the new key, and refuses to append when it does not.
func TestPostgres_RollingRotationTwoWriters(t *testing.T) {
	ctx := context.Background()
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	event := auditlog.Event{Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess}
	old := auditlog.ChainKey{ID: "1", Key: testChainKey}
	next := auditlog.ChainKey{ID: "2", Key: bytes.Repeat([]byte("n"), auditlog.MinChainKeyLen)}

	following := auditlog.New(store, auditlog.WithChainKeys(old), auditlog.WithNextChainKeys(next), auditlog.WithCursorKey(testCursorKey))
	unaware := auditlog.New(store, auditlog.WithChainKeys(old), auditlog.WithCursorKey(testCursorKey))
	rotated := auditlog.New(store, auditlog.WithChainKeys(next, old), auditlog.WithCursorKey(testCursorKey))

	for _, l := range []*auditlog.Logger{following, unaware, rotated, following, rotated, following} {
		require.NoError(t, l.LogE(ctx, event))
	}
	before, err := rotated.Verify(ctx, auditlog.Verification{})
	require.NoError(t, err)
	assert.Equal(t, "2", before.KeyID)

	assert.ErrorIs(t, unaware.LogE(ctx, event), auditlog.ErrChainHeadUnknown)
	head, err := following.Verify(ctx, auditlog.Verification{Anchor: before})
	require.NoError(t, err)
	assert.Equal(t, before, head, "the refused append left the chain unchanged")
}

// TestPostgres_RetentionPastTheAnchor prunes the anchored event and later
// ones: verification reports the pruned anchor, not a cut tail.
func TestPostgres_RetentionPastTheAnchor(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	store := postgresstore.New(pool)
	l := newLogger(t, pool)
	start := time.Now().Add(-time.Hour).UTC()
	logAt := func(at time.Time) {
		require.NoError(t, l.LogE(ctx, auditlog.Event{Actor: "agent", Action: "x", Resource: "r", Status: auditlog.StatusSuccess, Timestamp: at}))
	}
	logAt(start)
	logAt(start.Add(time.Minute))
	anchor, err := l.Verify(ctx, auditlog.Verification{})
	require.NoError(t, err)
	logAt(start.Add(2 * time.Minute))
	logAt(start.Add(3 * time.Minute))
	logAt(time.Now())

	deleted, watermark, err := store.DeleteBefore(ctx, start.Add(30*time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 4, deleted, "retention removed the anchored event and two after it")

	_, err = l.Verify(ctx, auditlog.Verification{Watermark: watermark, Anchor: anchor})
	require.ErrorIs(t, err, auditlog.ErrAnchorPruned)
	assert.NotErrorIs(t, err, auditlog.ErrChainBroken)
}
