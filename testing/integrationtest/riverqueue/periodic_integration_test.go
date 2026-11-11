//go:build integration

package riverqueue_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/queue/riverqueue/v2/periodic"
)

type scopeKey struct{}

func periodicPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	migrateRiver(t, pool)
	return pool
}

func runRegistry(t *testing.T, r *periodic.Registry, pool *pgxpool.Pool) {
	t.Helper()
	client, err := r.Client(pool, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- periodic.Run(client)(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done, "stop")
	})
}

func TestIntegration_PeriodicTasksRunOnStartAndKeepRunning(t *testing.T) {
	pool := periodicPool(t)
	ran := make(chan struct{}, 16)
	r := periodic.New(periodic.Options{Schema: "public", MaxAttempts: 3})
	r.Every("test.often", 2*time.Second, func(context.Context) error {
		ran <- struct{}{}
		return nil
	})
	runRegistry(t, r, pool)
	for runs := 0; runs < 3; runs++ {
		select {
		case <-ran:
		case <-time.After(20 * time.Second):
			t.Fatalf("periodic task ran %d times, want it to keep running", runs)
		}
	}
}

func TestIntegration_PerOrgTasksRunWithTheirOrganizationAsScope(t *testing.T) {
	pool := periodicPool(t)
	ran := make(chan string, 8)
	r := periodic.New(periodic.Options{Schema: "public", MaxAttempts: 3, OrgContext: func(ctx context.Context, orgID string) (context.Context, error) {
		return context.WithValue(ctx, scopeKey{}, orgID), nil
	}})
	r.EveryPerOrg("test.per_org", time.Hour, func(context.Context) ([]string, error) {
		return []string{"o1", "o2"}, nil
	}, func(ctx context.Context, org string) error {
		if scope, _ := ctx.Value(scopeKey{}).(string); scope != org {
			t.Errorf("task for %s ran with scope %q", org, scope)
		}
		ran <- org
		return nil
	})
	runRegistry(t, r, pool)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case org := <-ran:
			seen[org] = true
		case <-time.After(20 * time.Second):
			t.Fatalf("per-organization tasks ran for %v, want o1 and o2", seen)
		}
	}
}
