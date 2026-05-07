package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/leaderelection"
)

// stubElector is a no-op Elector that reports a fixed leader state.
type stubElector struct{ leader bool }

func (s *stubElector) Run(ctx context.Context, _ leaderelection.Callbacks) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *stubElector) IsLeader() bool { return s.leader }

func TestNewLeaderModule_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil elector")
		}
	}()
	newLeaderModule(nil)
}

func TestWithLeaderElection_PanicsOnNil(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil elector")
		}
	}()
	b.WithLeaderElection(nil)
}

func TestWithLeaderElection_PopulatesInfrastructure(t *testing.T) {
	e := &stubElector{leader: true}
	m := newLeaderModule(e)
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Same(t, e, infra.Leader)
}

func TestWithLeaderElection_RegistersOnBuilder(t *testing.T) {
	e := &stubElector{}
	b := New("test", "v1", BaseConfig{}).WithLeaderElection(e)
	assert.Same(t, e, b.leaderElector)
}
