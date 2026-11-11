package httpx_test

import (
	"testing"
	"time"

	"github.com/bds421/rho-kit/httpx/v2"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"3", 3 * time.Second, true},
		{" 0.25 ", time.Second, true},
		{"Thu, 01 Jan 2026 12:00:30 GMT", 30 * time.Second, true},
		{"Thu, 01 Jan 2026 11:00:00 GMT", 0, true},
		{"", 0, false},
		{"-1", 0, false},
		{"soon", 0, false},
	}
	for _, c := range cases {
		if got, ok := httpx.ParseRetryAfter(c.value, now); got != c.want || ok != c.ok {
			t.Errorf("ParseRetryAfter(%q) = %s, %v; want %s, %v", c.value, got, ok, c.want, c.ok)
		}
	}
}
