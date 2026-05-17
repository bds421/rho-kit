package timeoutbudget

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_PanicsOnInvalidArgs(t *testing.T) {
	assert.Panics(t, func() { New(nil, time.Second) })                  //nolint:staticcheck // intentional nil-ctx contract test
	assert.Panics(t, func() { New(context.Background(), 0) })
	assert.Panics(t, func() { New(context.Background(), -time.Second) })
}

func TestRemaining_DecreasesOverTime(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	_, b, cancel := New(context.Background(), time.Second, WithClock(clock.now))
	defer cancel()

	assert.InDelta(t, time.Second, b.Remaining(), float64(2*time.Millisecond))

	clock.advance(600 * time.Millisecond)
	assert.InDelta(t, 400*time.Millisecond, b.Remaining(), float64(2*time.Millisecond))
}

func TestRemaining_ZeroWhenExhausted(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	_, b, cancel := New(context.Background(), 100*time.Millisecond, WithClock(clock.now))
	defer cancel()

	clock.advance(150 * time.Millisecond)
	assert.Equal(t, time.Duration(0), b.Remaining(), "exhausted budget reports 0, never negative")
}

func TestWithReservation_HoldsBackRemainingTime(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	_, b, cancel := New(context.Background(), time.Second, WithClock(clock.now))
	defer cancel()

	restore := b.WithReservation(200 * time.Millisecond)
	defer restore()

	assert.InDelta(t, 800*time.Millisecond, b.Remaining(), float64(2*time.Millisecond),
		"reserved time must not appear in Remaining")
	assert.Equal(t, 200*time.Millisecond, b.Reservation())

	restore()
	assert.InDelta(t, time.Second, b.Remaining(), float64(2*time.Millisecond),
		"restore must release the reservation")
}

func TestWithReservation_NestedReservations(t *testing.T) {
	_, b, cancel := New(context.Background(), time.Second)
	defer cancel()

	outer := b.WithReservation(100 * time.Millisecond)
	inner := b.WithReservation(50 * time.Millisecond)

	assert.Equal(t, 150*time.Millisecond, b.Reservation(),
		"nested reservations accumulate")

	inner()
	assert.Equal(t, 100*time.Millisecond, b.Reservation(),
		"inner restore drops back to outer reservation")
	outer()
	assert.Equal(t, time.Duration(0), b.Reservation())
}

func TestWithRemaining_GivesChildCtxDeadline(t *testing.T) {
	ctx, b, cancel := New(context.Background(), 200*time.Millisecond)
	defer cancel()

	childCtx, childCancel, err := b.WithRemaining(ctx)
	require.NoError(t, err)
	defer childCancel()

	deadline, ok := childCtx.Deadline()
	require.True(t, ok)
	assert.True(t, time.Until(deadline) <= 200*time.Millisecond)
	assert.True(t, time.Until(deadline) > 100*time.Millisecond,
		"child deadline must reflect remaining budget")
}

func TestWithRemaining_ReservationShortensChildDeadline(t *testing.T) {
	ctx, b, cancel := New(context.Background(), 200*time.Millisecond)
	defer cancel()

	restore := b.WithReservation(80 * time.Millisecond)
	defer restore()

	childCtx, childCancel, err := b.WithRemaining(ctx)
	require.NoError(t, err)
	defer childCancel()

	deadline, _ := childCtx.Deadline()
	rem := time.Until(deadline)
	assert.True(t, rem <= 120*time.Millisecond,
		"child deadline must account for the 80ms reservation; got %s", rem)
}

func TestWithRemaining_ExhaustedReturnsError(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	ctx, b, cancel := New(context.Background(), 100*time.Millisecond, WithClock(clock.now))
	defer cancel()

	clock.advance(150 * time.Millisecond)

	_, ctxCancel, err := b.WithRemaining(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBudgetExhausted)
	defer ctxCancel() // must be safe even on exhausted budget
}

func TestFromContext_RetrievesAttachedBudget(t *testing.T) {
	ctx, b, cancel := New(context.Background(), time.Second)
	defer cancel()

	got := FromContext(ctx)
	assert.Same(t, b, got, "FromContext returns the same Budget pointer")
}

func TestFromContext_NilOnPlainContext(t *testing.T) {
	assert.Nil(t, FromContext(context.Background()))
	assert.Nil(t, FromContext(nil)) //nolint:staticcheck // intentional nil-ctx contract test
}

func TestParentCancelPropagatesIntoBudgetCtx(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	ctx, _, cancel := New(parent, time.Hour)
	defer cancel()

	parentCancel()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("budget ctx must inherit parent cancel")
	}
}

// fakeClock is a deterministic clock for budget tests.
type fakeClock struct {
	t time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
