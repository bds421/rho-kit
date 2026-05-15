package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkMemoryCache_Get(b *testing.B) {
	mc := MustNewMemoryCache()
	ctx := context.Background()
	_ = mc.Set(ctx, "bench-key", []byte("bench-value"), time.Hour)

	b.ResetTimer()
	for b.Loop() {
		_, _ = mc.Get(ctx, "bench-key")
	}
}

func BenchmarkMemoryCache_Set(b *testing.B) {
	mc := MustNewMemoryCache()
	ctx := context.Background()
	val := []byte("bench-value")

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		_ = mc.Set(ctx, fmt.Sprintf("key-%d", i), val, time.Hour)
	}
}

func BenchmarkMemoryCache_GetMiss(b *testing.B) {
	mc := MustNewMemoryCache()
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_, _ = mc.Get(ctx, "nonexistent")
	}
}

func BenchmarkMemoryCache_SetOverwrite(b *testing.B) {
	mc := MustNewMemoryCache()
	ctx := context.Background()
	val := []byte("bench-value")

	b.ResetTimer()
	for b.Loop() {
		_ = mc.Set(ctx, "same-key", val, time.Hour)
	}
}
