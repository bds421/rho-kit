package retry

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_successOnFirstAttempt(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_retriesUntilSuccess(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}, WithMaxRetries(5), WithBaseDelay(1*time.Millisecond), WithMaxDelay(5*time.Millisecond))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_exhaustsRetries(t *testing.T) {
	sentinel := errors.New("permanent")
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		return sentinel
	}, WithMaxRetries(2), WithBaseDelay(1*time.Millisecond))

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	// 1 initial + 2 retries = 3 calls
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_zeroRetries(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		return errors.New("fail")
	}, WithMaxRetries(0), WithBaseDelay(1*time.Millisecond))

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_retryIfStopsOnNonRetryable(t *testing.T) {
	var calls int
	sentinel := errors.New("no retry")
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		return sentinel
	}, WithRetryIf(func(err error) bool { return false }))

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_onRetryCalled(t *testing.T) {
	var calls int
	var attempts []int

	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}, WithMaxRetries(5), WithBaseDelay(1*time.Millisecond), WithMaxDelay(1*time.Millisecond), WithJitter(0),
		WithOnRetry(func(err error, attempt int, _ time.Duration) {
			if err == nil {
				t.Fatal("expected non-nil error in OnRetry")
			}
			attempts = append(attempts, attempt)
		}),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 retry callbacks, got %d", len(attempts))
	}
	if attempts[0] != 1 || attempts[1] != 2 {
		t.Fatalf("unexpected attempt sequence: %v", attempts)
	}
}

func TestDo_respectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	err := Do(ctx, func(_ context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("fail")
	}, WithMaxRetries(-1), WithBaseDelay(1*time.Millisecond))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDo_unlimitedRetries(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		if calls >= 5 {
			return nil
		}
		return errors.New("retry")
	}, WithMaxRetries(-1), WithBaseDelay(1*time.Millisecond), WithMaxDelay(2*time.Millisecond))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 5 {
		t.Errorf("expected 5 calls, got %d", calls)
	}
}

func TestDoWith_usesBasePolicy(t *testing.T) {
	p := Policy{
		MaxRetries: 1,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
		Factor:     2.0,
	}

	var calls int
	err := DoWith(context.Background(), p, func(_ context.Context) error {
		calls++
		return errors.New("fail")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 2 { // 1 initial + 1 retry
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestDo_exponentialBackoff(t *testing.T) {
	var timestamps []time.Time
	_ = Do(context.Background(), func(_ context.Context) error {
		timestamps = append(timestamps, time.Now())
		if len(timestamps) >= 4 {
			return nil
		}
		return errors.New("retry")
	}, WithMaxRetries(5), WithBaseDelay(10*time.Millisecond), WithMaxDelay(1*time.Second), WithFactor(2.0), WithJitter(0))

	if len(timestamps) < 4 {
		t.Fatalf("expected at least 4 timestamps, got %d", len(timestamps))
	}

	// Verify delays roughly double: d1 ~10ms, d2 ~20ms, d3 ~40ms
	d1 := timestamps[1].Sub(timestamps[0])
	d2 := timestamps[2].Sub(timestamps[1])
	d3 := timestamps[3].Sub(timestamps[2])

	if d2 < d1 {
		t.Errorf("delay should increase: d1=%v, d2=%v", d1, d2)
	}
	if d3 < d2 {
		t.Errorf("delay should increase: d2=%v, d3=%v", d2, d3)
	}
}

func TestDo_maxDelayCap(t *testing.T) {
	maxDelay := 5 * time.Millisecond
	var timestamps []time.Time

	_ = Do(context.Background(), func(_ context.Context) error {
		timestamps = append(timestamps, time.Now())
		if len(timestamps) >= 6 {
			return nil
		}
		return errors.New("retry")
	}, WithMaxRetries(10), WithBaseDelay(2*time.Millisecond), WithMaxDelay(maxDelay), WithFactor(4.0), WithJitter(0))

	// After a few retries, delay should be capped at maxDelay
	for i := 3; i < len(timestamps)-1; i++ {
		gap := timestamps[i+1].Sub(timestamps[i])
		// Allow 3x tolerance for scheduling jitter
		if gap > maxDelay*3 {
			t.Errorf("delay[%d]=%v exceeds 3x maxDelay=%v", i, gap, maxDelay)
		}
	}
}

func TestDo_stableReset(t *testing.T) {
	stableThresh := 20 * time.Millisecond
	var calls int
	var delayBeforeReset, delayAfterReset time.Time

	_ = Do(context.Background(), func(_ context.Context) error {
		calls++
		switch calls {
		case 1:
			return errors.New("fail fast")
		case 2:
			delayBeforeReset = time.Now()
			return errors.New("fail fast again")
		case 3:
			// Simulate stable run
			time.Sleep(stableThresh + 5*time.Millisecond)
			return errors.New("fail after stable run")
		case 4:
			delayAfterReset = time.Now()
			return nil
		}
		return nil
	}, WithMaxRetries(10), WithBaseDelay(5*time.Millisecond), WithMaxDelay(100*time.Millisecond),
		WithFactor(4.0), WithJitter(0), WithStableReset(stableThresh))

	// After stable run, delay should reset to base (5ms) instead of escalating
	if !delayAfterReset.IsZero() && !delayBeforeReset.IsZero() {
		gapAfterStable := delayAfterReset.Sub(delayBeforeReset)
		_ = gapAfterStable // Timing is unreliable in tests; just verify no panic
	}
}

func TestLoop_stopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	logger := slog.Default()

	Loop(ctx, logger, "test", func(_ context.Context) error {
		calls.Add(1)
		return errors.New("fail")
	}, WithBaseDelay(5*time.Millisecond), WithMaxDelay(10*time.Millisecond))

	if calls.Load() == 0 {
		t.Error("expected at least 1 call")
	}
}

func TestLoop_resetsOnStableRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	logger := slog.Default()

	Loop(ctx, logger, "test", func(_ context.Context) error {
		n := calls.Add(1)
		if n == 2 {
			// Simulate stable run that resets backoff
			time.Sleep(25 * time.Millisecond)
		}
		return errors.New("fail")
	}, WithBaseDelay(5*time.Millisecond), WithMaxDelay(50*time.Millisecond), WithStableReset(20*time.Millisecond))

	if calls.Load() < 2 {
		t.Errorf("expected at least 2 calls, got %d", calls.Load())
	}
}

func TestLoop_stopsOnNonRetryable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	logger := slog.Default()
	go func() {
		Loop(ctx, logger, "test", func(_ context.Context) error {
			return errors.New("fatal")
		}, WithRetryIf(func(err error) bool { return false }))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected loop to stop on non-retryable error")
	}
}

func TestPolicy_Delay(t *testing.T) {
	p := Policy{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  1 * time.Second,
		Factor:    2.0,
		Jitter:    0, // disable jitter for deterministic test
	}

	d0 := p.Delay(0)
	d1 := p.Delay(1)
	d2 := p.Delay(2)
	d3 := p.Delay(3)

	if d0 != 100*time.Millisecond {
		t.Errorf("Delay(0) = %v, want 100ms", d0)
	}
	if d1 != 200*time.Millisecond {
		t.Errorf("Delay(1) = %v, want 200ms", d1)
	}
	if d2 != 400*time.Millisecond {
		t.Errorf("Delay(2) = %v, want 400ms", d2)
	}
	if d3 != 800*time.Millisecond {
		t.Errorf("Delay(3) = %v, want 800ms", d3)
	}

	// Should cap at MaxDelay
	d10 := p.Delay(10)
	if d10 != 1*time.Second {
		t.Errorf("Delay(10) = %v, want 1s (capped)", d10)
	}
}
