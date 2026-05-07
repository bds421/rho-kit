package leaderelection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeElector is a minimal Elector for exercising the Callbacks contract
// without a real backend. It transitions through Acquired → Lost when
// ctx cancels.
type fakeElector struct {
	leader bool
}

func (f *fakeElector) IsLeader() bool { return f.leader }

func (f *fakeElector) Run(ctx context.Context, cb Callbacks) error {
	defer func() {
		if cb.OnLost != nil {
			cb.OnLost()
		}
	}()
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	f.leader = true
	if cb.OnAcquired != nil {
		go cb.OnAcquired(leaderCtx)
	}

	<-ctx.Done()
	f.leader = false
	return ctx.Err()
}

func TestCallbacks_OnLostFiresEvenWithoutAcquired(t *testing.T) {
	var lost int
	cb := Callbacks{
		OnLost: func() { lost++ },
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	e := &fakeElector{}
	_ = e.Run(ctx, cb)
	assert.Equal(t, 1, lost, "OnLost must fire even when leadership was never acquired")
}

func TestCallbacks_OnAcquiredCtxCancelsOnRunReturn(t *testing.T) {
	var sawCtxDone bool
	doneCh := make(chan struct{})
	cb := Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			sawCtxDone = true
			close(doneCh)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	e := &fakeElector{}
	_ = e.Run(ctx, cb)
	<-doneCh

	assert.True(t, sawCtxDone, "OnAcquired's ctx must cancel when Run returns")
}
