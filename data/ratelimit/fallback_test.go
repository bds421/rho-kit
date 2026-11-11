package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

type fixedLimiter struct {
	err   error
	calls int
}

func (f *fixedLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	f.calls++
	return f.err == nil, 0, f.err
}

func TestFallbackLimitsAloneOnlyWhileTheSharedLimiterFails(t *testing.T) {
	primary := &fixedLimiter{err: errors.New("refused")}
	secondary := &fixedLimiter{}
	f := ratelimit.Fallback{Primary: primary, Secondary: secondary}
	if ok, _, err := f.Allow(t.Context(), "anonymous:192.0.2.1"); !ok || err != nil || secondary.calls != 1 {
		t.Fatalf("failure did not fall back: %v %v %d", ok, err, secondary.calls)
	}
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.Allow(gone, "anonymous:192.0.2.1"); !errors.Is(err, context.Canceled) || secondary.calls != 1 {
		t.Fatalf("a gone client was counted as a limiter failure: %v %d", err, secondary.calls)
	}
	primary.err = ratelimit.ValidateKey("")
	if _, _, err := f.Allow(t.Context(), ""); !errors.Is(err, ratelimit.ErrInvalidKey) || secondary.calls != 1 {
		t.Fatalf("an invalid key was counted as a limiter failure: %v %d", err, secondary.calls)
	}
	primary.err = nil
	if ok, _, err := f.Allow(t.Context(), "anonymous:192.0.2.1"); !ok || err != nil || secondary.calls != 1 {
		t.Fatalf("a working shared limiter was not asked alone: %v %v %d", ok, err, secondary.calls)
	}
}
