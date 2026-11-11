package ratelimit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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
	var log bytes.Buffer
	f := &ratelimit.Fallback{Primary: primary, Secondary: secondary, Logger: slog.New(slog.NewTextHandler(&log, nil))}
	for range 3 {
		if ok, _, err := f.Allow(t.Context(), "anonymous:192.0.2.1"); !ok || err != nil {
			t.Fatalf("failure did not fall back: %v %v", ok, err)
		}
	}
	if secondary.calls != 3 || strings.Count(log.String(), "limits alone") != 1 {
		t.Fatalf("the outage was not logged once: %d calls, log %q", secondary.calls, log.String())
	}
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.Allow(gone, "anonymous:192.0.2.1"); !errors.Is(err, context.Canceled) || secondary.calls != 3 {
		t.Fatalf("a gone client was counted as a limiter failure: %v %d", err, secondary.calls)
	}
	primary.err = ratelimit.ValidateKey("")
	if _, _, err := f.Allow(t.Context(), ""); !errors.Is(err, ratelimit.ErrInvalidKey) || secondary.calls != 3 {
		t.Fatalf("an invalid key was counted as a limiter failure: %v %d", err, secondary.calls)
	}
	primary.err = nil
	for range 2 {
		if ok, _, err := f.Allow(t.Context(), "anonymous:192.0.2.1"); !ok || err != nil || secondary.calls != 3 {
			t.Fatalf("a working shared limiter was not asked alone: %v %v %d", ok, err, secondary.calls)
		}
	}
	if strings.Count(log.String(), "reachable again") != 1 {
		t.Fatalf("the recovery was not logged once: %q", log.String())
	}
}
