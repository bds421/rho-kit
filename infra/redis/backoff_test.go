package redis

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWithBackoff_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	done := make(chan struct{})

	go func() {
		RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
			calls.Add(1)
			cancel()
			return errors.New("fail")
		})
		close(done)
	}()

	// Wait for the goroutine to finish — no time.Sleep.
	<-done
	assert.Equal(t, int32(1), calls.Load())
}

func TestRunWithBackoff_RestartsOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32

	go RunWithBackoff(ctx, slog.Default(), "test", func(_ context.Context) error {
		n := calls.Add(1)
		if n >= 3 {
			cancel()
			return nil
		}
		return errors.New("transient")
	})

	<-ctx.Done()
	require.Eventually(t, func() bool {
		return calls.Load() >= 3
	}, 5*time.Second, 10*time.Millisecond)
}
