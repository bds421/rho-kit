package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRateLimitMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	if m1.decisions != m2.decisions {
		t.Fatal("NewMetrics should reuse decisions collector on duplicate registration")
	}
	if m1.retryAfter != m2.retryAfter {
		t.Fatal("NewMetrics should reuse retry-after collector on duplicate registration")
	}
}

func TestRateLimitMetrics_Contract(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	metrics.observeDecision("public_api", rateLimitKindIP, rateLimitOutcomeAllowed)
	metrics.observeRetryAfter("public_api", rateLimitKindIP, 1)

	assertMetricLabels(t, reg, "http_ratelimit_decisions_total", []string{"kind", "limiter", "outcome"})
	assertMetricLabels(t, reg, "http_ratelimit_retry_after_seconds", []string{"kind", "limiter"})
}

func TestLimiterMetrics_RecordIPDecisions(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	rl := NewLimiter(1, time.Minute,
		WithMetrics(metrics),
		WithLimiterName("public_api"),
	)

	if allowed, _ := rl.allow("192.0.2.10"); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := rl.allow("192.0.2.10"); allowed {
		t.Fatal("second request should be rate-limited")
	}
	rl.allow("") //nolint:errcheck // invalid-client-IP metric path

	assertDecision(t, metrics, "public_api", rateLimitKindIP, rateLimitOutcomeAllowed, 1)
	assertDecision(t, metrics, "public_api", rateLimitKindIP, rateLimitOutcomeLimited, 1)
	assertDecision(t, metrics, "public_api", rateLimitKindIP, rateLimitOutcomeInvalidClientIP, 1)
}

func TestKeyedLimiterMetrics_RecordKeyedDecisions(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	rl := NewKeyedLimiter(1, time.Minute,
		WithKeyedMetrics(metrics),
		WithKeyedLimiterName("api_key"),
	)

	if allowed, _, err := rl.AllowKey("tenant-a"); err != nil || !allowed {
		t.Fatalf("first AllowKey = allowed %v, err %v; want allowed nil", allowed, err)
	}
	if allowed, _, err := rl.AllowKey("tenant-a"); err != nil || allowed {
		t.Fatalf("second AllowKey = allowed %v, err %v; want limited nil", allowed, err)
	}
	_, _, _ = rl.AllowKey("bad key")

	assertDecision(t, metrics, "api_key", rateLimitKindKeyed, rateLimitOutcomeAllowed, 1)
	assertDecision(t, metrics, "api_key", rateLimitKindKeyed, rateLimitOutcomeLimited, 1)
	assertDecision(t, metrics, "api_key", rateLimitKindKeyed, rateLimitOutcomeInvalidKey, 1)
}

func TestLimiterMetrics_RecordDegradationOutcomes(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(false)
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	rl := NewLimiter(1, time.Minute,
		WithMetrics(metrics),
		WithLimiterName("login"),
		WithDegradation(health, passthroughHandler{}),
	)
	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
	assertDecision(t, metrics, "login", rateLimitKindIP, rateLimitOutcomeDegradedPassthrough, 1)
}

func TestLimiterMetrics_RejectUnsafeLimiterNames(t *testing.T) {
	assertPanic(t, func() { WithLimiterName("bad name") })
	assertPanic(t, func() { WithKeyedLimiterName("bad\nname") })
	assertPanic(t, func() { WithMetrics(nil) })
	assertPanic(t, func() { WithKeyedMetrics(nil) })
}

func TestKeyedActiveKeysGauge_ReflectsShardLength(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	rl := NewKeyedLimiter(100, time.Minute,
		WithKeyedMetrics(metrics),
		WithKeyedLimiterName("api_key"),
	)

	// No traffic yet — gauge should be 0.
	if got := activeKeysSample(t, reg, "api_key"); got != 0 {
		t.Fatalf("initial active-keys = %v, want 0", got)
	}

	// Three distinct keys map to up to 3 shards; sum across all shards
	// must equal the number of distinct keys regardless of FNV bucket.
	for _, key := range []string{"tenant-a", "tenant-b", "tenant-c"} {
		if allowed, _, err := rl.AllowKey(key); err != nil || !allowed {
			t.Fatalf("AllowKey(%q) = allowed %v, err %v", key, allowed, err)
		}
	}

	if got := activeKeysSample(t, reg, "api_key"); got != 3 {
		t.Fatalf("active-keys = %v, want 3", got)
	}

	// Repeated AllowKey for an existing key must NOT grow the gauge.
	for range 5 {
		_, _, _ = rl.AllowKey("tenant-a")
	}
	if got := activeKeysSample(t, reg, "api_key"); got != 3 {
		t.Fatalf("after duplicates active-keys = %v, want 3", got)
	}
}

func TestKeyedActiveKeysGauge_TracksMultipleLimitersDistinctNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	rlA := NewKeyedLimiter(100, time.Minute,
		WithKeyedMetrics(metrics),
		WithKeyedLimiterName("api_key"),
	)
	rlB := NewKeyedLimiter(100, time.Minute,
		WithKeyedMetrics(metrics),
		WithKeyedLimiterName("login"),
	)

	_, _, _ = rlA.AllowKey("tenant-a")
	_, _, _ = rlA.AllowKey("tenant-b")
	_, _, _ = rlB.AllowKey("user-1")

	if got := activeKeysSample(t, reg, "api_key"); got != 2 {
		t.Fatalf("api_key active-keys = %v, want 2", got)
	}
	if got := activeKeysSample(t, reg, "login"); got != 1 {
		t.Fatalf("login active-keys = %v, want 1", got)
	}
}

func TestKeyedActiveKeysGauge_NotEmittedWithoutLimiters(t *testing.T) {
	reg := prometheus.NewRegistry()
	_ = NewMetrics(WithRegisterer(reg))

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == "http_ratelimit_keyed_limiter_active_keys" {
			if len(mf.GetMetric()) != 0 {
				t.Fatalf("expected no samples with zero limiters, got %d", len(mf.GetMetric()))
			}
		}
	}
}

func TestKeyedActiveKeysGauge_IdempotentRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	if m1.activeKeys != m2.activeKeys {
		t.Fatal("duplicate NewMetrics on same registerer must share the active-keys collector")
	}

	// A limiter registered through m1's hooks must also surface in m2's view.
	rl := NewKeyedLimiter(10, time.Minute,
		WithKeyedMetrics(m1),
		WithKeyedLimiterName("shared"),
	)
	_, _, _ = rl.AllowKey("k1")

	if got := activeKeysSample(t, reg, "shared"); got != 1 {
		t.Fatalf("active-keys = %v, want 1", got)
	}
}

func activeKeysSample(t *testing.T, reg *prometheus.Registry, limiter string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "http_ratelimit_keyed_limiter_active_keys" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lbl := range metric.GetLabel() {
				if lbl.GetName() == "limiter" && lbl.GetValue() == limiter {
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	return 0
}

func assertDecision(t *testing.T, m *Metrics, limiter, kind, outcome string, want float64) {
	t.Helper()
	got := testutil.ToFloat64(m.decisions.WithLabelValues(limiter, kind, outcome))
	if got != want {
		t.Fatalf("decision %s/%s/%s = %v, want %v", limiter, kind, outcome, got, want)
	}
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}

func assertMetricLabels(t *testing.T, reg *prometheus.Registry, family string, want []string) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather returned %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != family {
			continue
		}
		if len(mf.GetMetric()) == 0 {
			t.Fatalf("metric family %s has no samples", family)
		}
		labels := make([]string, 0, len(mf.GetMetric()[0].GetLabel()))
		for _, label := range mf.GetMetric()[0].GetLabel() {
			labels = append(labels, label.GetName())
		}
		want = slices.Clone(want)
		slices.Sort(labels)
		slices.Sort(want)
		if len(labels) != len(want) {
			t.Fatalf("labels for %s = %v, want %v", family, labels, want)
		}
		for i := range want {
			if labels[i] != want[i] {
				t.Fatalf("labels for %s = %v, want %v", family, labels, want)
			}
		}
		return
	}
	t.Fatalf("metric family %s not found", family)
}
