package redis

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWithBackoff_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	done := make(chan error, 1)

	go func() {
		done <- RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
			calls.Add(1)
			cancel()
			return errors.New("fail")
		})
	}()

	err := <-done
	assert.Equal(t, int32(1), calls.Load())
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRunWithBackoff_RestartsOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan error, 1)

	go func() {
		done <- RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
			n := calls.Add(1)
			if n >= 3 {
				cancel()
				return nil
			}
			return errors.New("transient")
		})
	}()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, calls.Load(), int32(3))
}

func TestRunWithBackoff_ReturnsNilOnGracefulCompletion(t *testing.T) {
	ctx := context.Background()
	err := RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
		return nil
	})
	assert.NoError(t, err)
}

func TestRunWithBackoff_CancelWinsOverLastBusinessError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
			if calls.Add(1) == 1 {
				cancel()
			}
			return errors.New("business failure")
		})
	}()
	err := <-done
	assert.ErrorIs(t, err, context.Canceled)
}
