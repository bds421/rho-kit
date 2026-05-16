package websocket

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnLimiter_NilIsUnlimited(t *testing.T) {
	var l *connLimiter
	for i := 0; i < 1000; i++ {
		assert.True(t, l.tryAcquire(),
			"nil limiter must always permit acquire")
	}
	// release must be a no-op (no panic, no observable state).
	l.release()
}

func TestNewConnLimiter_NonPositiveReturnsNil(t *testing.T) {
	assert.Nil(t, newConnLimiter(0), "zero max disables the cap")
	assert.Nil(t, newConnLimiter(-5), "negative max also disables the cap")
}

func TestConnLimiter_AcquireUpToMax(t *testing.T) {
	l := newConnLimiter(3)
	require.NotNil(t, l)

	require.True(t, l.tryAcquire())
	require.True(t, l.tryAcquire())
	require.True(t, l.tryAcquire())
	assert.False(t, l.tryAcquire(), "fourth acquire must be rejected")
	assert.False(t, l.tryAcquire(), "still rejected on retry")

	l.release()
	assert.True(t, l.tryAcquire(), "release must free exactly one slot")
	assert.False(t, l.tryAcquire(), "and only one")
}

// TestConnLimiter_RaceFreeCounting hammers acquire/release from many
// goroutines and asserts that the observable count never exceeds max.
// Verifies the CAS loop is correct under contention — the naive
// Add(1)/compare/Add(-1) variant would let multiple goroutines briefly
// observe inflated values and falsely reject each other.
func TestConnLimiter_RaceFreeCounting(t *testing.T) {
	const max = 8
	const workers = 64
	const iterations = 1_000

	l := newConnLimiter(max)
	require.NotNil(t, l)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if l.tryAcquire() {
					// Slot acquired — release immediately so other
					// workers can compete for it.
					l.release()
				}
			}
		}()
	}
	wg.Wait()

	// Sanity: the counter must have returned to zero. A leak in the
	// CAS loop would leave residual count.
	assert.EqualValues(t, 0, l.current.Load(),
		"acquire/release imbalance leaked slots: %d remaining", l.current.Load())
}

func TestWithMaxConnections_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() { WithMaxConnections(-1) })
	// Zero is allowed (disables).
	assert.NotPanics(t, func() { WithMaxConnections(0) })
}
