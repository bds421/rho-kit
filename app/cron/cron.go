// Package cron is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/runtime/v2/cron]. Services that need
// a managed cron scheduler pass [cron.Module] to
// [app.Builder.With]; services that don't, do not import this
// package.
//
// When the leader-election bridge module (app/leader) is also
// registered, cron jobs gate on `elector.IsLeader()` automatically
// — only the leader replica runs scheduled work. Other replicas
// keep HTTP, consumers, and other modules running normally. The
// lookup goes through the [app.ElectorProvider] capability so
// app/cron does not import app/leader.
//
// Retrieve the scheduler inside the [app.RouterFunc] via
// [Scheduler]:
//
//	app.New(name).
//	    With(leader.Module(elector)). // optional
//	    With(cron.Module()).
//	    Run(func(infra app.Infrastructure) http.Handler {
//	        cron.Scheduler(infra).Add(...)
//	        ...
//	    })
package cron

import (
	"context"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	kitcron "github.com/bds421/rho-kit/runtime/v2/cron"
)

// ResourceSchedulerKey is the [app.Infrastructure.Resource] key
// under which [Module] publishes its *kitcron.Scheduler.
const ResourceSchedulerKey = "github.com/bds421/rho-kit/app/cron.scheduler"

// ModuleName is the registered Module.Name() value.
const ModuleName = "cron"

// Module returns an [app.Module] that constructs and supervises a
// *kitcron.Scheduler. The scheduler starts before the public HTTP
// server and drains during graceful shutdown (it waits for running
// jobs to complete).
//
// Pass kitcron options (e.g. kitcron.WithMaxJitter) through opts.
// Panics if any option is nil.
//
// If app/leader.Module is also registered on the Builder, the
// scheduler gates every job on the leader's IsLeader() result.
func Module(opts ...kitcron.Option) app.Module {
	for _, opt := range opts {
		if opt == nil {
			panic("app/cron: Module option must not be nil")
		}
	}
	return &cronModule{userOpts: append([]kitcron.Option(nil), opts...)}
}

type cronModule struct {
	userOpts []kitcron.Option

	// initialized during Init
	scheduler *kitcron.Scheduler
}

func (m *cronModule) Name() string { return ModuleName }

func (m *cronModule) Init(_ context.Context, mc app.ModuleContext) error {
	opts := append([]kitcron.Option(nil), m.userOpts...)
	// Optional leader gating: query the registered modules for an
	// ElectorProvider. The Builder pre-populates ModuleContext's
	// initialized-modules map in registration order, so the leader
	// module's Elector() is callable here as long as the leader
	// module was registered BEFORE cron.Module — which is the
	// natural shape anyway.
	if leader, ok := lookupElector(mc); ok {
		opts = append(opts, kitcron.WithLeaderGate(leader.IsLeader))
	}
	m.scheduler = kitcron.New(mc.Logger, opts...)
	mc.Runner.Add("cron-scheduler", m.scheduler)
	return nil
}

func (m *cronModule) Populate(infra *app.Infrastructure) {
	if m.scheduler != nil {
		infra.SetResource(ResourceSchedulerKey, m.scheduler)
	}
}

func (m *cronModule) Stop(_ context.Context) error { return nil }

func (m *cronModule) HealthChecks() []health.DependencyCheck { return nil }

// lookupElector queries the registered modules for the
// ElectorProvider capability so app/cron stays independent of
// app/leader. The lookup is optional — services that don't
// register a leader module run cron unguarded.
//
// A foreign ElectorProvider whose Elector() returns a nil interface
// is treated as absent: app/leader always returns a non-nil elector,
// but a third-party module under the same name must not crash Init by
// having the leader.IsLeader method value dereference a nil interface.
func lookupElector(mc app.ModuleContext) (electorLike, bool) {
	m := mc.LookupModule("leader-election")
	if m == nil {
		return nil, false
	}
	ep, ok := m.(app.ElectorProvider)
	if !ok {
		return nil, false
	}
	e := ep.Elector()
	if e == nil {
		return nil, false
	}
	return e, true
}

// electorLike is a tiny structural mirror over
// leaderelection.Elector — IsLeader is the only method cron
// needs, and depending on the full interface would defeat the
// point of the capability indirection.
type electorLike interface {
	IsLeader() bool
}

// Scheduler returns the *kitcron.Scheduler published by [Module]
// under [ResourceSchedulerKey], or nil if [Module] was not
// registered with the Builder.
func Scheduler(infra app.Infrastructure) *kitcron.Scheduler {
	v, ok := infra.Resource(ResourceSchedulerKey)
	if !ok {
		return nil
	}
	s, _ := v.(*kitcron.Scheduler)
	return s
}
