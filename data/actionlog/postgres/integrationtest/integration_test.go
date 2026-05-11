//go:build integration

package integrationtest

import (
	"context"
	"io/fs"
	"net"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/data/actionlog/postgres/v2"
	"github.com/bds421/rho-kit/data/v2/actionlog"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "actionlog_test")
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// goose runs against *sql.DB; pgx exposes a sql.DB-compatible adapter
	// via stdlib.OpenDBFromPool. We use it only for the migration step,
	// then drop the *sql.DB handle. The pgxpool below remains the live
	// connection pool used by the store.
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(postgresstore.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func TestPostgres_Live_RoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := postgresstore.New(pool)
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
	pool := openAndMigrate(t, dsn)
	store := postgresstore.New(pool)
	log := actionlog.New(store, actionlog.NewStaticSecrets("k1", map[string][]byte{
		"k1": []byte("0123456789abcdef0123456789abcdef"),
	}))

	written, err := log.Append(context.Background(), actionlog.Entry{
		TenantID: "tenant-1", Actor: "agent-7", Action: "user.delete",
		Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	// Reach in with raw SQL — DBA-with-edit-rights model.
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	_, err = sqlDB.Exec(
		"UPDATE action_log_entries SET actor = $1 WHERE id = $2",
		"impostor", written.ID,
	)
	require.NoError(t, err)

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
	pool := openAndMigrate(t, dsn)
	store := postgresstore.New(pool)
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
