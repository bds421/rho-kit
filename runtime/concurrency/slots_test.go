package concurrency

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tryAcquire acquires with a context that has already ended, so a full
// bound fails at once instead of waiting.
func tryAcquire(t *testing.T, s *KeyedSlots[string], key string) (func(), error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return s.Acquire(ctx, key)
}

func TestKeyedSlots_BoundPerKey(t *testing.T) {
	s := NewKeyedSlots[string](4, 1)
	release, err := s.Acquire(t.Context(), "ada")
	require.NoError(t, err)
	_, err = tryAcquire(t, s, "ada")
	require.ErrorIs(t, err, context.Canceled, "a key's second slot")
	other, err := s.Acquire(t.Context(), "bob")
	require.NoError(t, err, "another key has its own slots")
	release()
	again, err := s.Acquire(t.Context(), "ada")
	require.NoError(t, err)
	again()
	other()
}

func TestKeyedSlots_BoundInAll(t *testing.T) {
	s := NewKeyedSlots[string](2, 2)
	a, err := s.Acquire(t.Context(), "ada")
	require.NoError(t, err)
	b, err := s.Acquire(t.Context(), "bob")
	require.NoError(t, err)
	_, err = tryAcquire(t, s, "cyd")
	require.ErrorIs(t, err, context.Canceled, "a third slot of all")
	assert.Equal(t, 2, s.Keys(), "a key that gave up is forgotten")
	a()
	c, err := s.Acquire(t.Context(), "cyd")
	require.NoError(t, err)
	b()
	c()
}

func TestKeyedSlots_CancelWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewKeyedSlots[string](1, 1)
		hold, err := s.Acquire(t.Context(), "ada")
		require.NoError(t, err)
		gone := errors.New("caller left")
		ctx, cancel := context.WithCancelCause(t.Context())
		got := make(chan error, 1)
		go func() {
			_, err := s.Acquire(ctx, "bob")
			got <- err
		}()
		synctest.Wait() // bob waits for the slot of all
		require.Equal(t, 2, s.Keys())
		cancel(gone)
		require.ErrorIs(t, <-got, gone, "the cause of the context's end")
		assert.Equal(t, 1, s.Keys())
		hold()
	})
}

func TestKeyedSlots_ReleaseOnceAndForgetIdleKeys(t *testing.T) {
	s := NewKeyedSlots[string](1, 1)
	release, err := s.Acquire(t.Context(), "ada")
	require.NoError(t, err)
	release()
	release() // a second call must not give back a slot it does not hold
	other, err := s.Acquire(t.Context(), "bob")
	require.NoError(t, err)
	_, err = tryAcquire(t, s, "cyd")
	require.ErrorIs(t, err, context.Canceled, "the second release freed nothing")
	other()
	assert.Zero(t, s.Keys())
}

func TestNewKeyedSlots_PanicsOnBounds(t *testing.T) {
	assert.Panics(t, func() { NewKeyedSlots[string](0, 1) })
	assert.Panics(t, func() { NewKeyedSlots[string](1, 0) })
}
