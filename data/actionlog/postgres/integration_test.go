//go:build integration

package postgres

import (
	"context"
	"io/fs"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/data/actionlog"
)

// startPostgres mirrors the kit's other integration tests
// (infra/sqldb/pgx/integration_test.go). One container per test so a
// rogue test that ALTERs the table can't poison the next.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func openAndMigrate(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	return db
}

func TestPostgres_Live_RoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	db := openAndMigrate(t, dsn)
	store := New(db)
	logger := actionlog.New(store, actionlog.NewStaticSecrets("k1", map[string][]byte{
		"k1": []byte("0123456789abcdef0123456789abcdef"),
	}))

	written, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "tenant-1",
		Actor:    "agent-7",
		Action:   "user.delete",
		Resource: "users/42",
		Outcome:  actionlog.OutcomeSuccess,
		Metadata: map[string]any{"requested_by": "ops@example.com"},
	})
	require.NoError(t, err)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, written, got)
}

func TestPostgres_Live_TamperDetected(t *testing.T) {
	dsn := startPostgres(t)
	db := openAndMigrate(t, dsn)
	store := New(db)
	log := actionlog.New(store, actionlog.NewStaticSecrets("k1", map[string][]byte{
		"k1": []byte("0123456789abcdef0123456789abcdef"),
	}))

	written, err := log.Append(context.Background(), actionlog.Entry{
		TenantID: "tenant-1", Actor: "agent-7", Action: "user.delete",
		Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	// Reach in with raw SQL — DBA-with-edit-rights model.
	require.NoError(t, db.Exec(
		"UPDATE action_log_entries SET actor = ? WHERE id = ?",
		"impostor", written.ID,
	).Error)

	_, err = log.Get(context.Background(), written.ID)
	assert.ErrorIs(t, err, actionlog.ErrSignatureInvalid)
}

// TestPostgres_Live_ConcurrentFirstAppend is the regression test for
// the R2 first-append-not-serialised finding: with SELECT FOR UPDATE
// only, two concurrent first-appends for a tenant with no rows yet
// would both build seq=1 and one would fail the unique constraint.
// The pg_advisory_xact_lock(hashtext(tenant_id)) added in
// AppendChained makes the build+persist atomic from the first row on.
func TestPostgres_Live_ConcurrentFirstAppend(t *testing.T) {
	dsn := startPostgres(t)
	db := openAndMigrate(t, dsn)
	store := New(db)
	log := actionlog.New(store, actionlog.NewStaticSecrets("k1", map[string][]byte{
		"k1": []byte("0123456789abcdef0123456789abcdef"),
	}))

	const n = 50
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := log.Append(context.Background(), actionlog.Entry{
				TenantID: "tenant-first",
				Actor:    "agent",
				Action:   "x",
				Outcome:  actionlog.OutcomeSuccess,
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Empty(t, errs, "no concurrent first-append should fail")

	require.NoError(t, log.VerifyChain(context.Background(), "tenant-first"))

	got, err := log.List(context.Background(), actionlog.Query{TenantID: "tenant-first", Limit: n + 1})
	require.NoError(t, err)
	assert.Len(t, got, n)
}
