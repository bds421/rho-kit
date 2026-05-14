package tokenbucket

import (
	"context"
	"testing"
	"time"
)

var (
	benchAllowed    bool
	benchRetryAfter time.Duration
	benchAllowErr   error
)

func BenchmarkAllowAllowed(b *testing.B) {
	now := time.Unix(0, 0)
	limiter := Open(1, 1, WithClock(func() time.Time { return now }), WithoutSweeper())
	defer func() { _ = limiter.Close() }()

	var allowed bool
	var retryAfter time.Duration
	var err error
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		now = now.Add(time.Second)
		allowed, retryAfter, err = limiter.Allow(context.Background(), "tenant-123")
		if err != nil || !allowed || retryAfter != 0 {
			b.Fatalf("Allow = %v, %v, %v", allowed, retryAfter, err)
		}
	}
	benchAllowed = allowed
	benchRetryAfter = retryAfter
	benchAllowErr = err
}

func BenchmarkAllowDenied(b *testing.B) {
	now := time.Unix(0, 0)
	limiter := Open(1, 1, WithClock(func() time.Time { return now }), WithoutSweeper())
	defer func() { _ = limiter.Close() }()
	if allowed, _, err := limiter.Allow(context.Background(), "tenant-123"); err != nil || !allowed {
		b.Fatalf("initial Allow = %v, %v", allowed, err)
	}

	var allowed bool
	var retryAfter time.Duration
	var err error
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		allowed, retryAfter, err = limiter.Allow(context.Background(), "tenant-123")
		if err != nil || allowed || retryAfter <= 0 {
			b.Fatalf("Allow = %v, %v, %v", allowed, retryAfter, err)
		}
	}
	benchAllowed = allowed
	benchRetryAfter = retryAfter
	benchAllowErr = err
}
