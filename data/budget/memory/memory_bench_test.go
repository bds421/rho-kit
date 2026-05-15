package memory_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bds421/rho-kit/data/v2/budget/memory"
)

// BenchmarkBudget_Consume_HotKey measures the fast path: every
// Consume call hits the same key, so the bucket pointer is in cache
// and the sync.Map.Load is a fast atomic read. The kit aims to keep
// this on the order of 100ns/op so memory-budget can sit in front of
// every request without becoming the dominant latency contributor
// (L167).
func BenchmarkBudget_Consume_HotKey(b *testing.B) {
	bg := memory.New(int64(b.N+1000), time.Hour, memory.WithoutSweeper())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = bg.Consume(ctx, "hot", 1)
	}
}

// BenchmarkBudget_Consume_DistinctKeys measures the cold path:
// every Consume call hits a new key, so the bucket has to be
// allocated and inserted into the sync.Map. This is the worst case
// for memory-budget — a tenant-per-request workload should not hit
// this shape, but the benchmark exists to bound how bad it gets.
func BenchmarkBudget_Consume_DistinctKeys(b *testing.B) {
	bg := memory.New(int64(b.N+1000), time.Hour, memory.WithoutSweeper())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = bg.Consume(ctx, "k-"+strconv.Itoa(i), 1)
	}
}

// BenchmarkBudget_Consume_Parallel measures the contention path with
// many goroutines hitting the same key. The kit uses a per-bucket
// mutex inside Consume, so this benchmark is essentially measuring
// mutex throughput on a single key. Useful for diagnosing
// regressions in the loadOrInitBucket retry loop.
func BenchmarkBudget_Consume_Parallel(b *testing.B) {
	bg := memory.New(int64(1<<30), time.Hour, memory.WithoutSweeper())
	ctx := context.Background()
	var n atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _, _ = bg.Consume(ctx, "hot", 1)
			n.Add(1)
		}
	})
}
