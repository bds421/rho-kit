package retry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestDo_OnRetryPanicDoesNotStopRetries(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	},
		WithMaxRetries(2),
		WithBaseDelay(time.Millisecond),
		WithMaxDelay(time.Millisecond),
		WithJitter(0),
		WithOnRetry(func(error, int, time.Duration) {
			panic("retry hook exploded")
		}),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected retry after OnRetry panic, got %d calls", calls)
	}
}

func TestDo_RetryIfPanicStopsRetrying(t *testing.T) {
	sentinel := errors.New("transient")
	var calls int
	err := Do(context.Background(), func(context.Context) error {
		calls++
		return sentinel
	}, WithRetryIf(func(error) bool {
		panic("predicate exploded")
	}))

	if !errors.Is(err, sentinel) {
		t.Fatalf("expected original error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected no retry after RetryIf panic, got %d calls", calls)
	}
}

func TestDo_DelayOverridePanicFallsBack(t *testing.T) {
	var calls int
	err := Do(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	},
		WithMaxRetries(2),
		WithBaseDelay(time.Millisecond),
		WithMaxDelay(time.Millisecond),
		WithJitter(0),
		WithDelayOverride(func(error) time.Duration {
			panic("delay hook exploded")
		}),
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected retry after DelayOverride panic, got %d calls", calls)
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

func TestDo_surfacesFnErrorAlongsideCancelledCtx(t *testing.T) {
	// fn returns a real business error AND ctx is cancelled in the same
	// iteration. The business error must not be silently swallowed —
	// callers need both signals (fn's error wins for inspection;
	// errors.Is(err, context.Canceled) still works).
	ctx, cancel := context.WithCancel(context.Background())
	businessErr := errors.New("downstream rejected payload")
	err := Do(ctx, func(_ context.Context) error {
		cancel()
		return businessErr
	}, WithMaxRetries(-1), WithBaseDelay(1*time.Millisecond))

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, businessErr) {
		t.Fatalf("expected wrapped business error, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
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
	base := 5 * time.Millisecond
	var calls int
	// Capture the scheduled delay handed to OnRetry rather than measuring
	// wall-clock gaps (which are unreliable). With Jitter=0 the backoff
	// sequence is deterministic, so the recorded delays prove the reset.
	var delays []time.Duration

	_ = Do(context.Background(), func(_ context.Context) error {
		calls++
		switch calls {
		case 1:
			return errors.New("fail fast")
		case 2:
			return errors.New("fail fast again")
		case 3:
			// Simulate a stable run that crosses the StableReset threshold.
			time.Sleep(stableThresh + 5*time.Millisecond)
			return errors.New("fail after stable run")
		case 4:
			return nil
		}
		return nil
	}, WithMaxRetries(10), WithBaseDelay(base), WithMaxDelay(100*time.Millisecond),
		WithFactor(4.0), WithJitter(0), WithStableReset(stableThresh),
		WithOnRetry(func(_ error, _ int, delay time.Duration) {
			delays = append(delays, delay)
		}))

	// Three failures preceded a retry, so OnRetry fired three times.
	if len(delays) != 3 {
		t.Fatalf("expected 3 recorded delays, got %d: %v", len(delays), delays)
	}

	// Deterministic backoff (Factor=4, Jitter=0): the first two delays
	// escalate from BaseDelay, but the stable run before the third failure
	// must reset the sequence back to BaseDelay.
	if delays[0] != base {
		t.Errorf("first delay = %v, want BaseDelay %v", delays[0], base)
	}
	if delays[1] != base*4 {
		t.Errorf("second delay = %v, want %v (escalated)", delays[1], base*4)
	}
	// The load-bearing assertion: without StableReset this would be
	// base*16 (80ms); the reset drops it back to BaseDelay.
	if delays[2] != base {
		t.Errorf("post-stable delay = %v, want it reset to BaseDelay %v", delays[2], base)
	}
	if delays[2] >= delays[1] {
		t.Errorf("post-stable delay %v should drop below pre-stable delay %v", delays[2], delays[1])
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

func TestLoop_stopsOnMaxElapsedTime(t *testing.T) {
	// Generous ctx timeout acts only as a safety net so the test can't hang;
	// the loop must stop because MaxElapsedTime is reached, well before it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, slog.Default(), "test", func(_ context.Context) error {
			calls.Add(1)
			return errors.New("fail")
		}, WithBaseDelay(5*time.Millisecond), WithMaxDelay(10*time.Millisecond),
			WithJitter(0), WithMaxElapsedTime(40*time.Millisecond))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected loop to stop after MaxElapsedTime, ran until ctx cancel")
	}

	if calls.Load() == 0 {
		t.Error("expected at least 1 call")
	}
}

func TestLoop_NilLoggerNormalized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	// nil logger must be normalized to slog.Default(); first failure must
	// not panic.
	Loop(ctx, nil, "test", func(_ context.Context) error {
		calls.Add(1)
		return errors.New("fail")
	}, WithBaseDelay(5*time.Millisecond), WithMaxDelay(10*time.Millisecond))

	if calls.Load() == 0 {
		t.Error("expected at least 1 call")
	}
}

func TestLoop_RestartsAfterWorkerPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, slog.Default(), "test", func(context.Context) error {
			if calls.Add(1) == 1 {
				panic("worker exploded")
			}
			cancel()
			return nil
		}, WithBaseDelay(time.Millisecond), WithMaxDelay(time.Millisecond), WithJitter(0))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not return after restart")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected Loop to restart after panic, got %d calls", got)
	}
}

func TestLoop_OnRetryPanicDoesNotStopLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, slog.Default(), "test", func(context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("transient")
			}
			cancel()
			return nil
		},
			WithBaseDelay(time.Millisecond),
			WithMaxDelay(time.Millisecond),
			WithJitter(0),
			WithOnRetry(func(error, int, time.Duration) {
				panic("retry hook exploded")
			}),
		)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not continue after OnRetry panic")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected Loop to continue after OnRetry panic, got %d calls", got)
	}
}

func TestLoop_RetryIfPanicStopsLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, slog.Default(), "test", func(context.Context) error {
			calls.Add(1)
			return errors.New("transient")
		}, WithRetryIf(func(error) bool {
			panic("predicate exploded")
		}))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not stop after RetryIf panic")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one call before RetryIf panic stopped loop, got %d", got)
	}
}

func TestLoop_DelayOverridePanicFallsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, slog.Default(), "test", func(context.Context) error {
			if calls.Add(1) == 1 {
				return errors.New("transient")
			}
			cancel()
			return nil
		},
			WithBaseDelay(time.Millisecond),
			WithMaxDelay(time.Millisecond),
			WithJitter(0),
			WithDelayOverride(func(error) time.Duration {
				panic("delay hook exploded")
			}),
		)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not continue after DelayOverride panic")
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected Loop to continue after DelayOverride panic, got %d calls", got)
	}
}

func TestLoop_PanicsOnNilFn(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Loop called with nil fn")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Loop(ctx, slog.Default(), "test", nil)
}

func TestDo_PanicsOnNilFn(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Do called with nil fn")
		}
	}()
	_ = Do(context.Background(), nil)
}

func TestDo_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Do called with nil option")
		}
	}()
	_ = Do(context.Background(), func(context.Context) error { return nil }, nil)
}

func TestDoWith_PanicsOnNilFn(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when DoWith called with nil fn")
		}
	}()
	_ = DoWith(context.Background(), DefaultPolicy(), nil)
}

func TestDoWith_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when DoWith called with nil option")
		}
	}()
	_ = DoWith(context.Background(), DefaultPolicy(), func(context.Context) error { return nil }, nil)
}

func TestDefaultPolicies_ReturnFreshValues(t *testing.T) {
	p := DefaultPolicy()
	p.MaxRetries = 0
	p.RetryIf = nil

	fresh := DefaultPolicy()
	if fresh.MaxRetries != 3 {
		t.Fatalf("DefaultPolicy() reflected caller mutation: %+v", fresh)
	}
	if fresh.RetryIf == nil {
		t.Fatal("DefaultPolicy() lost RetryIf predicate")
	}

	worker := WorkerPolicy()
	worker.MaxRetries = 0
	if got := WorkerPolicy().MaxRetries; got != -1 {
		t.Fatalf("WorkerPolicy() reflected caller mutation: MaxRetries=%d", got)
	}
}

func TestDoWith_PanicsOnInvalidPolicy(t *testing.T) {
	defer func() {
		rcv := recover()
		if rcv == nil {
			t.Fatal("expected panic for invalid policy")
		}
		msg, ok := rcv.(string)
		if !ok {
			t.Fatalf("panic type = %T, want string", rcv)
		}
		if !strings.Contains(msg, "retry: Do policy") || !strings.Contains(msg, "base delay") {
			t.Fatalf("panic = %q, want context + Validate() diagnosis", msg)
		}
	}()
	_ = DoWith(context.Background(), Policy{
		MaxRetries: 1,
		BaseDelay:  0,
		MaxDelay:   time.Millisecond,
		Factor:     1,
	}, func(context.Context) error { return nil })
}

func TestMustValidatePolicyIncludesValidateError(t *testing.T) {
	defer func() {
		rcv := recover()
		msg, ok := rcv.(string)
		if !ok {
			t.Fatalf("panic type = %T, want string", rcv)
		}
		if !strings.Contains(msg, "retry: Do policy") {
			t.Fatalf("panic missing context prefix: %q", msg)
		}
		if !strings.Contains(msg, "base delay") {
			t.Fatalf("panic missing Validate() diagnosis: %q", msg)
		}
	}()

	mustValidatePolicy("retry: Do policy", Policy{})
}

func TestLoop_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic when Loop called with nil option")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Loop(ctx, slog.Default(), "test", func(context.Context) error { return nil }, nil)
}

func TestLoop_PanicsOnInvalidPolicyOption(t *testing.T) {
	defer func() {
		rcv := recover()
		if rcv == nil {
			t.Fatal("expected panic for invalid policy option")
		}
		msg, ok := rcv.(string)
		if !ok {
			t.Fatalf("panic = %T, want string", rcv)
		}
		if msg != "retry: WithMaxElapsedTime requires d >= 0" {
			t.Fatalf("panic = %q, want %q", msg, "retry: WithMaxElapsedTime requires d >= 0")
		}
		if strings.Contains(msg, "-1s") {
			t.Fatalf("panic reflected invalid duration: %q", msg)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Loop(ctx, slog.Default(), "test", func(context.Context) error { return nil }, WithMaxElapsedTime(-time.Second))
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

func TestPolicy_ValidateRejectsInvalidValues(t *testing.T) {
	valid := DefaultPolicy()
	tests := []struct {
		name      string
		p         Policy
		forbidden string
	}{
		{name: "base delay", p: func() Policy { p := valid; p.BaseDelay = 0; return p }(), forbidden: "0s"},
		{name: "max delay", p: func() Policy { p := valid; p.MaxDelay = 0; return p }(), forbidden: "0s"},
		{name: "factor", p: func() Policy { p := valid; p.Factor = 0.5; return p }(), forbidden: "0.5"},
		{name: "negative jitter", p: func() Policy { p := valid; p.Jitter = -0.1; return p }(), forbidden: "-0.1"},
		{name: "large jitter", p: func() Policy { p := valid; p.Jitter = 1.1; return p }(), forbidden: "1.1"},
		{name: "stable reset", p: func() Policy { p := valid; p.StableReset = -time.Millisecond; return p }(), forbidden: "-1ms"},
		{name: "max elapsed", p: func() Policy { p := valid; p.MaxElapsedTime = -time.Millisecond; return p }(), forbidden: "-1ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if strings.Contains(err.Error(), tt.forbidden) {
				t.Fatalf("validation error reflected invalid value: %q", err)
			}
		})
	}
}

func TestPolicy_NewBackoffPanicsOnInvalidPolicy(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic for invalid policy")
		}
	}()
	_ = (Policy{}).NewBackoff()
}

func TestPolicy_DelayPanicsOnInvalidPolicy(t *testing.T) {
	defer func() {
		if rcv := recover(); rcv == nil {
			t.Fatal("expected panic for invalid policy")
		}
	}()
	_ = (Policy{}).Delay(0)
}

func TestDo_SuccessWinsOverConcurrentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := Do(ctx, func(_ context.Context) error {
		cancel()
		return nil
	}, WithMaxRetries(0), WithBaseDelay(time.Millisecond), WithMaxDelay(time.Millisecond))
	if err != nil {
		t.Fatalf("successful attempt must win over concurrent cancel, got %v", err)
	}
}

func TestDo_DelayOverrideClampedToMaxDelay(t *testing.T) {
	var observed time.Duration
	var calls int
	err := Do(context.Background(), func(_ context.Context) error {
		calls++
		if calls >= 2 {
			return nil
		}
		return errors.New("transient")
	},
		WithMaxRetries(3),
		WithBaseDelay(time.Millisecond),
		WithMaxDelay(20*time.Millisecond),
		WithJitter(0),
		WithDelayOverride(func(error) time.Duration {
			return time.Hour // hostile Retry-After
		}),
		WithOnRetry(func(_ error, _ int, wait time.Duration) {
			observed = wait
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if observed != 20*time.Millisecond {
		t.Fatalf("DelayOverride wait = %v, want clamped to MaxDelay=20ms", observed)
	}
}

func TestDo_BackoffCancelJoinsLastError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	business := errors.New("still failing")
	var calls int
	err := Do(ctx, func(_ context.Context) error {
		calls++
		if calls == 1 {
			// cancel during the upcoming backoff sleep
			go func() {
				time.Sleep(5 * time.Millisecond)
				cancel()
			}()
		}
		return business
	},
		WithMaxRetries(5),
		WithBaseDelay(50*time.Millisecond),
		WithMaxDelay(50*time.Millisecond),
		WithJitter(0),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, business) {
		t.Fatalf("expected last fn error joined, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled joined, got %v", err)
	}
}

func TestLoop_RedactsWorkerErrorsInLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	secret := "password=super-secret-dsn"
	n := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, logger, "worker", func(context.Context) error {
			n++
			if n >= 2 {
				cancel()
			}
			return errors.New(secret)
		}, WithMaxRetries(5), WithBaseDelay(time.Millisecond), WithMaxDelay(time.Millisecond), WithJitter(0))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("Loop did not stop after cancellation")
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("Loop logs leaked raw worker error: %q", out)
	}
	if !strings.Contains(out, "redacted") && !strings.Contains(out, "error=") {
		// redact.Error stamps contain "redacted error"
		t.Fatalf("expected redacted error attr in logs, got %q", out)
	}
}

func TestBackoff_NextClampsPostJitterToMaxDelay(t *testing.T) {
	p := Policy{
		MaxRetries: 10,
		BaseDelay:  30 * time.Second,
		MaxDelay:   30 * time.Second,
		Factor:     2,
		Jitter:     0.5, // cenkalti can return up to MaxInterval*(1+jitter)
	}
	bo := p.NewBackoff()
	for i := 0; i < 20; i++ {
		d := bo.Next()
		if d > p.MaxDelay {
			t.Fatalf("Next() = %v exceeds MaxDelay %v on iteration %d", d, p.MaxDelay, i)
		}
	}
}
