package cache

import (
	"context"
	"testing"
	"time"
)

func BenchmarkGetOrCompute_Hit(b *testing.B) {
	mc := MustNewMemoryCache()
	cc, err := NewComputeCache[string](mc, "bench:")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = cc.Close() }()

	ctx := context.Background()
	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "value", 10 * time.Minute, nil
	}

	// Prime the cache.
	if _, err := cc.GetOrCompute(ctx, "key", fn); err != nil {
		b.Fatal(err)
	}
	mc.Sync()

	b.ResetTimer()
	for b.Loop() {
		_, _ = cc.GetOrCompute(ctx, "key", fn)
	}
}

func BenchmarkGetOrCompute_Miss(b *testing.B) {
	mc := MustNewMemoryCache()
	cc, err := NewComputeCache[string](mc, "bench:")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = cc.Close() }()

	ctx := context.Background()
	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "value", 0, nil // zero TTL so entry never persists
	}

	b.ResetTimer()
	for b.Loop() {
		_, _ = cc.GetOrCompute(ctx, "key", fn)
	}
}

func BenchmarkGetOrCompute_Stale(b *testing.B) {
	mc := MustNewMemoryCache()
	cc, err := NewComputeCache[string](mc, "bench:",
		WithStaleTTL(10*time.Minute),
	)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = cc.Close() }()

	ctx := context.Background()

	// Prime the cache with an already-expired entry by using a very short TTL.
	primeFn := func(ctx context.Context) (string, time.Duration, error) {
		return "stale-value", 1 * time.Nanosecond, nil
	}
	if _, err := cc.GetOrCompute(ctx, "key", primeFn); err != nil {
		b.Fatal(err)
	}
	mc.Sync()
	time.Sleep(10 * time.Millisecond) // ensure primary TTL has expired

	refreshFn := func(ctx context.Context) (string, time.Duration, error) {
		return "fresh-value", 10 * time.Minute, nil
	}

	b.ResetTimer()
	for b.Loop() {
		_, _ = cc.GetOrCompute(ctx, "key", refreshFn)
		cc.Wait() // wait for background refresh to complete between iterations
	}
}
