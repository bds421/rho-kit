package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/bds421/rho-kit/core/v2/clock"
)

func TestSystem_ReturnsRealTime(t *testing.T) {
	now := clock.System()
	before := time.Now()
	got := now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System() returned %v outside [%v, %v]", got, before, after)
	}
}

func TestOrSystem_NilFallsBackToSystem(t *testing.T) {
	resolved := clock.OrSystem(nil)
	if resolved == nil {
		t.Fatal("OrSystem(nil) returned nil")
	}
	// resolved must produce real wall-clock time.
	got := resolved()
	if got.IsZero() {
		t.Fatal("OrSystem(nil) returned zero time — should fall back to time.Now")
	}
}

func TestOrSystem_PassesThrough(t *testing.T) {
	want := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	custom := clock.Fixed(want)
	resolved := clock.OrSystem(custom)
	if got := resolved(); !got.Equal(want) {
		t.Fatalf("OrSystem passed-through clock returned %v, want %v", got, want)
	}
}

func TestFixed_AlwaysReturnsSameInstant(t *testing.T) {
	want := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	c := clock.Fixed(want)
	for i := 0; i < 10; i++ {
		if got := c(); !got.Equal(want) {
			t.Fatalf("Fixed call %d: got %v, want %v", i, got, want)
		}
	}
}

func TestStub_AdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	s := clock.NewStub(start)

	if got := s.Now(); !got.Equal(start) {
		t.Fatalf("initial Now = %v, want %v", got, start)
	}

	s.Advance(2 * time.Hour)
	want := start.Add(2 * time.Hour)
	if got := s.Now(); !got.Equal(want) {
		t.Fatalf("after Advance: got %v, want %v", got, want)
	}

	reset := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Set(reset)
	if got := s.Now(); !got.Equal(reset) {
		t.Fatalf("after Set: got %v, want %v", got, reset)
	}
}

func TestStub_FuncReadsLatest(t *testing.T) {
	s := clock.NewStub(time.Unix(0, 0))
	now := s.Func()
	s.Advance(time.Minute)
	if got := now(); got.Unix() != 60 {
		t.Fatalf("Stub.Func did not observe Advance: got %v", got)
	}
}

func TestStub_ConcurrentReadsAndWrites(t *testing.T) {
	s := clock.NewStub(time.Unix(0, 0))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.Advance(time.Microsecond)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = s.Now()
			}
		}()
	}
	wg.Wait()
}

func TestStub_ConcurrentAdvanceLosesNoIncrements(t *testing.T) {
	const (
		goroutines = 8
		perG       = 1000
		step       = time.Microsecond
	)
	start := time.Unix(0, 0)
	s := clock.NewStub(start)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				s.Advance(step)
			}
		}()
	}
	wg.Wait()

	want := start.Add(time.Duration(goroutines*perG) * step)
	if got := s.Now(); !got.Equal(want) {
		t.Fatalf("concurrent Advance lost increments: got %v, want %v (delta %v)",
			got, want, want.Sub(got))
	}
}
