// Package leader is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/infra/v2/leaderelection].
//
// Services that need leader election (typically to gate cron jobs
// or outbox relay loops to a single replica) pass [leader.Module]
// to [app.Builder.With]. Services that don't, do not import this
// package.
//
// Retrieve the elector inside the [app.RouterFunc] via [Elector]:
//
//	app.New(name).
//	    With(leader.Module(myElector)).
//	    With(cron.Module(...)). // reads the leader via mc.Module
//	    Run(func(infra app.Infrastructure) http.Handler { ... })
package leader

import (
	"context"
	"database/sql"
	"fmt"

	kitpostgres "github.com/bds421/rho-kit/app/postgres/v2"
	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ResourceElectorKey is the [app.Infrastructure.Resource] key
// under which [Module] publishes its leaderelection.Elector.
const ResourceElectorKey = "github.com/bds421/rho-kit/app/leader.elector"

// ModuleName is the registered Module.Name() value, exposed for
// the Builder's cron block (and other adapters) that look up
// leader presence by name without importing this package.
const ModuleName = "leader-election"

// PGAdvisory returns an [app.Module] backed by a Postgres advisory-lock
// elector. Register before [github.com/bds421/rho-kit/app/cron/v2].Module
// so cron jobs gate on [leaderelection.Elector.IsLeader].
//
// Panics if db is nil or key is empty.
func PGAdvisory(db *sql.DB, key string, opts ...pgadvisory.Option) app.Module {
	if db == nil {
		panic("app/leader: PGAdvisory requires a non-nil *sql.DB")
	}
	if key == "" {
		panic("app/leader: PGAdvisory requires a non-empty key")
	}
	return Module(pgadvisory.New(db, key, opts...))
}

// PGAdvisoryFromPostgres returns an [app.Module] that resolves the
// advisory-lock elector from the registered [app/postgres] module at
// Init time. Register postgres before this module and before
// [github.com/bds421/rho-kit/app/cron/v2].Module.
//
// Panics if key is empty.
func PGAdvisoryFromPostgres(key string, opts ...pgadvisory.Option) app.Module {
	if key == "" {
		panic("app/leader: PGAdvisoryFromPostgres requires a non-empty key")
	}
	return &pgAdvisoryModule{key: key, opts: append([]pgadvisory.Option(nil), opts...)}
}

type pgAdvisoryModule struct {
	key  string
	opts []pgadvisory.Option
	leaderModule
}

func (m *pgAdvisoryModule) Init(ctx context.Context, mc app.ModuleContext) error {
	pm := mc.LookupModule(kitpostgres.ModuleName)
	if pm == nil {
		return fmt.Errorf("app/leader: PGAdvisoryFromPostgres requires postgres module registered before leader")
	}
	// Module map is pre-populated before any Init runs, so presence alone
	// does not mean postgres.Init completed. Prefer an explicit readiness
	// check when available; otherwise fall through and let SQLDB panic.
	if ready, ok := pm.(kitpostgres.SQLDBReadyProvider); ok && !ready.SQLDBReady() {
		return fmt.Errorf("app/leader: PGAdvisoryFromPostgres requires postgres module registered before leader")
	}
	sp, ok := pm.(kitpostgres.SQLDBProvider)
	if !ok {
		return fmt.Errorf("app/leader: postgres module does not expose SQLDB")
	}
	m.elector = pgadvisory.New(sp.SQLDB(), m.key, m.opts...)
	return m.leaderModule.Init(ctx, mc)
}

// Module returns an [app.Module] that runs the supplied Elector
// under the lifecycle Runner. The Elector's IsLeader() can then be
// called from any goroutine to gate work to the leader replica.
//
// Panics if elector is nil.
func Module(elector leaderelection.Elector) app.Module {
	if elector == nil {
		panic("app/leader: Module requires a non-nil Elector")
	}
	return &leaderModule{elector: elector}
}

type leaderModule struct {
	elector leaderelection.Elector
}

func (m *leaderModule) Name() string { return ModuleName }

func (m *leaderModule) Init(_ context.Context, mc app.ModuleContext) error {
	mc.Runner.AddFunc("leader-election", func(ctx context.Context) error {
		return m.elector.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(ctx context.Context) {
				mc.Logger.Info("leader-election: acquired leadership")
				<-ctx.Done()
			},
			OnLost: func() {
				mc.Logger.Info("leader-election: lost leadership")
			},
		})
	})
	return nil
}

func (m *leaderModule) Populate(infra *app.Infrastructure) {
	infra.SetResource(ResourceElectorKey, m.elector)
}

func (m *leaderModule) Stop(_ context.Context) error { return nil }

func (m *leaderModule) HealthChecks() []health.DependencyCheck { return nil }

// Elector exposes the elector to other modules at Init time via
// the [app.ElectorProvider] capability interface. The Builder's
// cron block looks it up through this method without importing
// this package.
func (m *leaderModule) Elector() leaderelection.Elector { return m.elector }

// Elector returns the leaderelection.Elector published by [Module]
// under [ResourceElectorKey], or nil if [Module] was not
// registered with the Builder.
func Elector(infra app.Infrastructure) leaderelection.Elector {
	v, ok := infra.Resource(ResourceElectorKey)
	if !ok {
		return nil
	}
	e, _ := v.(leaderelection.Elector)
	return e
}
