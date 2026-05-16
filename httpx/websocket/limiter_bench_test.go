package websocket

import (
	"sync"
	"testing"
)

// BenchmarkConnLimiter_TryAcquireRelease pins the per-connection
// cost of the CAS-loop limiter introduced in wave 157. The
// limiter sits in the hot path for every websocket connection
// attempt; a regression here is a goroutine-per-connection cost
// multiplier under contention.
func BenchmarkConnLimiter_TryAcquireRelease(b *testing.B) {
	l := newConnLimiter(int64(b.N) + 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if l.tryAcquire() {
			l.release()
		}
	}
}

// BenchmarkConnLimiter_Parallel exercises the CAS loop under
// concurrent contention, which is the limiter's actual production
// shape. Regression here indicates the CAS retry rate has grown.
func BenchmarkConnLimiter_Parallel(b *testing.B) {
	l := newConnLimiter(1000)
	b.ReportAllocs()
	b.ResetTimer()
	var wg sync.WaitGroup
	b.RunParallel(func(pb *testing.PB) {
		wg.Add(1)
		defer wg.Done()
		for pb.Next() {
			if l.tryAcquire() {
				l.release()
			}
		}
	})
	wg.Wait()
}
