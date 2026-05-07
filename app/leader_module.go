package app

import (
	"context"
	"log/slog"

	"github.com/bds421/rho-kit/infra/leaderelection"
)

// leaderModule runs a [leaderelection.Elector] under the lifecycle
// runner so cron jobs (and other leader-gated work) can call
// `elector.IsLeader()` from any goroutine.
type leaderModule struct {
	BaseModule

	elector leaderelection.Elector
	log     *slog.Logger
}

func newLeaderModule(e leaderelection.Elector) *leaderModule {
	if e == nil {
		panic("app: leader-election module requires a non-nil Elector")
	}
	return &leaderModule{
		BaseModule: NewBaseModule("leader-election"),
		elector:    e,
	}
}

func (m *leaderModule) Init(_ context.Context, mc ModuleContext) error {
	m.log = mc.Logger
	mc.Runner.AddFunc("leader-election", func(ctx context.Context) error {
		return m.elector.Run(ctx, leaderelection.Callbacks{
			OnAcquired: func(_ context.Context) {
				mc.Logger.Info("leader-election: acquired leadership")
			},
			OnLost: func() {
				mc.Logger.Info("leader-election: lost leadership")
			},
		})
	})
	return nil
}

func (m *leaderModule) Populate(infra *Infrastructure) {
	infra.Leader = m.elector
}
