package redisstore

import (
	"testing"
	"time"
)

// TestTTLMillisRoundUp guards the TTL precision fix: Redis SET PX accepts
// integer milliseconds. Truncating a sub-millisecond duration with
// ttl.Milliseconds() used to round to 0 and cause Redis to reject the
// command, breaking the lock acquisition.
func TestTTLMillisRoundUp(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int64
	}{
		{1 * time.Nanosecond, 1},
		{500 * time.Microsecond, 1},
		{1 * time.Millisecond, 1},
		{500 * time.Millisecond, 500},
		{999*time.Millisecond + 1*time.Microsecond, 1000},
		{1 * time.Second, 1000},
		{60 * time.Second, 60_000},
	}
	for _, c := range cases {
		if got := ttlMillisRoundUp(c.in); got != c.want {
			t.Errorf("ttlMillisRoundUp(%s) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTTLRoundUp_PreservesAtLeastOneMilli(t *testing.T) {
	if got := ttlRoundUp(1 * time.Nanosecond); got != time.Millisecond {
		t.Errorf("ttlRoundUp(1ns) = %s, want 1ms", got)
	}
	if got := ttlRoundUp(2*time.Second + 250*time.Microsecond); got != 2001*time.Millisecond {
		t.Errorf("ttlRoundUp(2s 250µs) = %s, want 2001ms", got)
	}
}
