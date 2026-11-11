package ratelimit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	limiter "github.com/bds421/rho-kit/data/v2/ratelimit"
	"github.com/bds421/rho-kit/data/v2/ratelimit/gcra"
	"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
)

var admitted = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func call(h http.Handler, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/cves", nil)
	r.RemoteAddr = peer
	r.Header.Set("X-Forwarded-For", "random-spoof")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestTheBudgetIsPerPeerAndRefills(t *testing.T) {
	now := time.Now()
	local := gcra.New(2*time.Second, 2, gcra.WithClock(func() time.Time { return now }), gcra.WithoutSweeper())
	h := ratelimit.Limit{Limiter: local, Policy: ratelimit.PerMinute(60)}.Middleware(admitted)
	for range 2 {
		if w := call(h, "127.0.0.1:123"); w.Code != http.StatusNoContent || w.Header().Get("RateLimit-Policy") != `"minute";q=60;w=60` {
			t.Fatal("initial budget", w.Code, w.Header())
		}
	}
	denied := call(h, "127.0.0.1:456")
	if denied.Code != http.StatusTooManyRequests || denied.Header().Get("Retry-After") == "" {
		t.Fatal("another port or a forwarded header got around the budget", denied.Code)
	}
	if got, want := denied.Header().Get("RateLimit"), `"minute";r=0;t=`+denied.Header().Get("Retry-After"); got != want {
		t.Fatalf("RateLimit %q, want %q", got, want)
	}
	if call(h, "127.0.0.2:123").Code != http.StatusNoContent {
		t.Fatal("another peer shares the budget")
	}
	now = now.Add(time.Second)
	if call(h, "127.0.0.1:123").Code != http.StatusNoContent {
		t.Fatal("no refill")
	}
}

type failing struct{}

func (failing) Allow(context.Context, string) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}

func TestAFailingLimiterAnswers503WithAShortRetry(t *testing.T) {
	var refusals, outages int
	h := ratelimit.Limit{
		Limiter:     failing{},
		Policy:      ratelimit.PerMinute(60),
		Refuse:      func(http.ResponseWriter, *http.Request) { refusals++ },
		Unavailable: func(w http.ResponseWriter, _ *http.Request) { outages++; w.WriteHeader(http.StatusServiceUnavailable) },
	}.Middleware(admitted)
	if w := call(h, "203.0.113.9:1"); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" || outages != 1 || refusals != 0 {
		t.Fatal("a failing limiter was not reported as unavailable", w.Code, w.Header())
	}
	fallback := ratelimit.Limit{Limiter: &limiter.Fallback{Primary: failing{}, Secondary: gcra.New(time.Minute, 1, gcra.WithoutSweeper())}, Policy: ratelimit.PerMinute(60)}.Middleware(admitted)
	if call(fallback, "203.0.113.9:1").Code != http.StatusNoContent {
		t.Fatal("public access refused while Redis is down")
	}
	if call(fallback, "203.0.113.9:1").Code != http.StatusTooManyRequests {
		t.Fatal("no budget while Redis is down")
	}
}

func TestEveryKeyMustAdmitTheRequest(t *testing.T) {
	local := gcra.New(time.Minute, 1, gcra.WithoutSweeper())
	h := ratelimit.Limit{Limiter: local, Policy: ratelimit.PerMinute(60), Keys: func(r *http.Request) []string {
		return []string{"address:" + ratelimit.ClientAddress(r), r.Header.Get("X-Member")}
	}}.Middleware(admitted)
	as := func(peer, member string) int {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Member", member)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if as("127.0.0.1:1", "ada") != http.StatusNoContent {
		t.Fatal("initial budget")
	}
	if as("127.0.0.1:2", "grace") != http.StatusTooManyRequests {
		t.Fatal("another member behind the same address multiplied its budget")
	}
	if as("127.0.0.2:1", "ada") != http.StatusTooManyRequests {
		t.Fatal("a member spread the budget over another address")
	}
	if as("127.0.0.3:1", "") != http.StatusBadRequest {
		t.Fatal("an invalid key was not refused")
	}
	none := ratelimit.Limit{Limiter: local, Policy: ratelimit.PerMinute(60), Keys: func(*http.Request) []string { return nil }}.Middleware(admitted)
	if w := call(none, "127.0.0.4:1"); w.Code != http.StatusBadRequest {
		t.Fatal("a request without a budget was admitted", w.Code)
	}
}

func TestLimitFailsFastWithoutALimiterOrPolicy(t *testing.T) {
	for name, l := range map[string]ratelimit.Limit{
		"limiter": {Policy: ratelimit.PerMinute(1)},
		"policy":  {Limiter: failing{}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			l.Middleware(admitted)
		}()
	}
}
