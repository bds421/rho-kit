package interceptor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestDeadlineUnary_AppliesToCtxWithoutDeadline(t *testing.T) {
	icp := DeadlineUnary(50 * time.Millisecond)

	var observedDeadline time.Time
	_, err := icp(context.Background(), nil, nil,
		func(ctx context.Context, _ any) (any, error) {
			dl, ok := ctx.Deadline()
			require.True(t, ok, "interceptor must inject a deadline")
			observedDeadline = dl
			return nil, nil
		})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), observedDeadline, 100*time.Millisecond)
}

func TestDeadlineUnary_RespectsTighterClientDeadline(t *testing.T) {
	icp := DeadlineUnary(time.Hour)

	tight := time.Now().Add(20 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), tight)
	defer cancel()

	var observed time.Time
	_, err := icp(ctx, nil, nil,
		func(ctx context.Context, _ any) (any, error) {
			dl, ok := ctx.Deadline()
			require.True(t, ok)
			observed = dl
			return nil, nil
		})
	require.NoError(t, err)
	assert.True(t, observed.Equal(tight),
		"caller's tighter deadline must win; got %v want %v", observed, tight)
}

func TestDeadlineUnary_TightensLooserClientDeadline(t *testing.T) {
	icp := DeadlineUnary(50 * time.Millisecond)

	loose := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), loose)
	defer cancel()

	var observed time.Time
	_, err := icp(ctx, nil, nil,
		func(ctx context.Context, _ any) (any, error) {
			dl, _ := ctx.Deadline()
			observed = dl
			return nil, nil
		})
	require.NoError(t, err)
	assert.True(t, observed.Before(loose),
		"server cap must override looser caller deadline; got %v vs caller %v", observed, loose)
}

func TestDeadlineUnary_PanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero deadline")
		}
	}()
	DeadlineUnary(0)
}

// fakeStream satisfies grpc.ServerStream's Context() requirement; the
// rest of the interface is unused in this test.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestDeadlineStream_AppliesToCtxWithoutDeadline(t *testing.T) {
	icp := DeadlineStream(40 * time.Millisecond)

	var observed time.Time
	err := icp(nil, &fakeStream{ctx: context.Background()}, nil,
		func(_ any, ss grpc.ServerStream) error {
			dl, ok := ss.Context().Deadline()
			require.True(t, ok)
			observed = dl
			return nil
		})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(40*time.Millisecond), observed, 100*time.Millisecond)
}

func TestDeadlineUnary_HandlerErrorPropagates(t *testing.T) {
	// The interceptor must not swallow errors — only manage ctx.
	icp := DeadlineUnary(time.Second)
	want := errors.New("boom")
	_, err := icp(context.Background(), nil, nil,
		func(_ context.Context, _ any) (any, error) { return nil, want })
	assert.ErrorIs(t, err, want)
}
