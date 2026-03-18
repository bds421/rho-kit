package circuitbreaker

import (
	"testing"
	"time"
)

func BenchmarkExecute_Closed(b *testing.B) {
	cb := NewCircuitBreaker(100, time.Second)
	noop := func() error { return nil }

	b.ResetTimer()
	for b.Loop() {
		_ = cb.Execute(noop)
	}
}

func BenchmarkExecute_Open(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Hour)
	_ = cb.Execute(func() error { return errTrip })

	b.ResetTimer()
	for b.Loop() {
		_ = cb.Execute(func() error { return nil })
	}
}

func BenchmarkState(b *testing.B) {
	cb := NewCircuitBreaker(10, time.Second)

	b.ResetTimer()
	for b.Loop() {
		_ = cb.State()
	}
}

var errTrip = ErrCircuitOpen
