//go:build integration

package auditlogpg

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

var testRetentionKey = bytes.Repeat([]byte("r"), auditlog.MinRetentionKeyLen)

const (
	day           = 24 * time.Hour
	testRetention = 400 * day
	testMinimum   = 365 * day
)

func appendAged(t *testing.T, logger *auditlog.Logger, now time.Time, ages ...int) {
	t.Helper()
	for _, age := range ages {
		e := event("aged", auditlog.StatusSuccess)
		e.Timestamp = now.Add(-time.Duration(age) * day)
		require.NoError(t, logger.LogE(context.Background(), e))
	}
}

// A signed cut, decoded by a role with only the public key, removes what it
// names and writes the sealed record; the chain verifies from it, and the
// same cut again is refused.
func TestPostgres_ApplyCutWritesTheSealedRecord(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	appendAged(t, logger, now, 500, 450, 10)
	kept, err := logger.Verify(ctx, auditlog.Verification{})
	require.NoError(t, err)
	store := postgresstore.New(pool)

	c, err := auditlog.ComputeCut(ctx, store, []auditlog.ChainKey{{Key: testChainKey}}, auditlog.Verification{}, now.Add(-testRetention), now)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := auditlog.NewEd25519CutSigner(private)
	require.NoError(t, err)
	verifier, err := auditlog.NewEd25519CutVerifier(public)
	require.NoError(t, err)
	text, err := c.Encode(signer)
	require.NoError(t, err)
	decoded, err := auditlog.DecodeCut(text, verifier, now, time.Hour)
	require.NoError(t, err)

	removed, err := store.ApplyCut(ctx, decoded, testRetentionKey, testMinimum, now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	_, err = store.ApplyCut(ctx, decoded, testRetentionKey, testMinimum, now)
	require.ErrorIs(t, err, auditlog.ErrCutRefused)

	record, err := store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	assert.EqualValues(t, 2, record.Removed)
	assert.Equal(t, c.Watermark, record.Watermark)
	v := record.Verification(testRetention, testMinimum, now)
	v.Anchor = kept
	head, err := logger.Verify(ctx, v)
	require.NoError(t, err)
	assert.Equal(t, kept, head)

	_, err = pool.Exec(ctx, `UPDATE audit_log_retention SET removed = 1`)
	require.NoError(t, err)
	_, err = store.RetentionRecord(ctx, testRetentionKey)
	require.ErrorIs(t, err, auditlog.ErrRecordUnsealed)
}

// Logger.Prune in the caller's transaction rolls back with it and commits
// with it; a cut made before rows moved is refused.
func TestPostgres_PruneJoinsTheCallersTransaction(t *testing.T) {
	ctx := context.Background()
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	appendAged(t, logger, now, 500, 450, 440, 10)
	store := postgresstore.New(pool)
	stale, err := logger.ComputeCut(ctx, auditlog.Verification{}, now.Add(-445*day), now)
	require.NoError(t, err)

	errLater := errors.New("the caller's change fails")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		n, err := logger.Prune(postgresstore.WithTx(ctx, tx), testRetentionKey, testRetention, testMinimum, now)
		require.NoError(t, err)
		require.EqualValues(t, 3, n)
		return errLater
	})
	require.ErrorIs(t, err, errLater)
	assert.Len(t, actions(t, logger), 4, "the removal rolled back with the caller")
	record, err := store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	assert.True(t, record.IsZero())

	require.NoError(t, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := logger.Prune(postgresstore.WithTx(ctx, tx), testRetentionKey, testRetention, testMinimum, now)
		return err
	}))
	assert.Len(t, actions(t, logger), 1)
	_, err = store.ApplyCut(ctx, stale, testRetentionKey, testMinimum, now)
	require.ErrorIs(t, err, auditlog.ErrCutRefused)
	record, err = store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	_, err = logger.Verify(ctx, record.Verification(testRetention, testMinimum, now))
	require.NoError(t, err)
}
