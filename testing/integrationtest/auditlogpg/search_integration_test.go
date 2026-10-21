//go:build integration

package auditlogpg

import (
	"context"
	"io/fs"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

const searchMigration = 20261022000001

func TestPostgres_SearchMatchesFieldsAndMetadataValues(t *testing.T) {
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	ctx := context.Background()

	for _, e := range []auditlog.Event{
		{Actor: "user:Alice", Tenant: "a", Action: "member.added", Resource: "org:a/membership:1", Metadata: []byte(`{"details":{"role":"Billing Admin"}}`)},
		{Actor: "user:bob", Tenant: "a", Action: "group.renamed", Resource: "org:a/group:50%_off", Metadata: []byte(`{"details":{"name":"Ops"}}`)},
		{Actor: "user:alice", Tenant: "b", Action: "member.added", Resource: "org:b/membership:2"},
	} {
		e.Status = auditlog.StatusSuccess
		require.NoError(t, logger.LogE(ctx, e))
	}

	search := func(tenant, term string) []string {
		got, _, err := logger.List(ctx, auditlog.Filter{Tenant: tenant, Search: term}, "", 10)
		require.NoError(t, err)
		actions := make([]string, len(got))
		for i, e := range got {
			actions[i] = e.Tenant + "/" + e.Action
		}
		return actions
	}
	assert.Equal(t, []string{"a/member.added"}, search("a", "ALICE"))
	assert.Equal(t, []string{"a/member.added"}, search("a", "billing adm"))
	assert.Equal(t, []string{"a/group.renamed"}, search("a", "50%_"))
	assert.Empty(t, search("a", "5_%"))
	assert.Empty(t, search("a", "details"), "metadata keys are not searched")
	assert.Len(t, search("", "member.added"), 2)
	require.NoError(t, logger.VerifyChain(ctx))
}

func TestPostgres_SearchKeysetPaging(t *testing.T) {
	pool := openAndMigrate(t, startPostgres(t))
	logger := newLogger(t, pool)
	defer func() { _ = logger.Close() }()
	ctx := context.Background()

	same := time.Now().UTC().Truncate(time.Second)
	for i := range 9 {
		actor := "user:bob"
		if i%3 != 0 {
			actor = "user:carol"
		}
		require.NoError(t, logger.LogE(ctx, auditlog.Event{
			Timestamp: same.Add(-time.Duration(i/2) * time.Second), Actor: actor,
			Action: "x" + strconv.Itoa(i), Resource: "r", Status: auditlog.StatusSuccess,
		}))
	}

	seen := map[string]bool{}
	cursor := ""
	for {
		page, next, err := logger.List(ctx, auditlog.Filter{Search: "carol"}, cursor, 2)
		require.NoError(t, err)
		for _, e := range page {
			assert.Equal(t, "user:carol", e.Actor)
			assert.False(t, seen[e.ID], "no entry twice across pages")
			seen[e.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	assert.Len(t, seen, 6)
}

func TestPostgres_SearchMigrationBackfillsAndIndexes(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	provider := gooseProvider(t, pool)
	_, err = provider.UpTo(ctx, searchMigration-1)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
INSERT INTO audit_log_events (id, occurred_at, actor, action, resource, status, metadata, signed_metadata, hmac) VALUES
 ('signed', now(), 'user:dora', 'role.granted', 'org:1/role:7', 'success', NULL, convert_to('{"details":{"role":"Auditor"},"n":42}', 'UTF8'), '\x01'),
 ('empty', now(), 'user:erin', 'org.closed', 'org:1', 'success', NULL, '\x', '\x02'),
 ('legacy', now(), 'svc', 'export', 'org:1', 'success', '{"format":"csv"}', NULL, '\x03'),
 ('broken', now(), 'svc', 'import', 'org:1', 'success', NULL, '\xff00', '\x04')`)
	require.NoError(t, err)

	_, err = provider.Up(ctx)
	require.NoError(t, err)

	texts := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT id, search_text FROM audit_log_events`)
	require.NoError(t, err)
	for rows.Next() {
		var id, text string
		require.NoError(t, rows.Scan(&id, &text))
		texts[id] = text
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{
		"signed": "user:dora\nrole.granted\norg:1/role:7\n42\nAuditor",
		"empty":  "user:erin\norg.closed\norg:1",
		"legacy": "svc\nexport\norg:1\ncsv",
		"broken": "svc\nimport\norg:1",
	}, texts)

	var index string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_audit_log_events_search'`).Scan(&index))
	assert.Contains(t, index, "gin_trgm_ops")
}

func gooseProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	sub, err := fs.Sub(postgresstore.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	return provider
}
