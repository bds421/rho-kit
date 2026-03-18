package health

import (
	"context"
	"testing"
	"time"
)

func TestResolveVersion_EnvOverride(t *testing.T) {
	t.Setenv("APP_VERSION", "2.0.0")
	if got := ResolveVersion("1.0.0"); got != "2.0.0" {
		t.Errorf("ResolveVersion = %q, want 2.0.0", got)
	}
}

func TestResolveVersion_Fallback(t *testing.T) {
	t.Setenv("APP_VERSION", "")
	if got := ResolveVersion("1.0.0"); got != "1.0.0" {
		t.Errorf("ResolveVersion = %q, want 1.0.0", got)
	}
}

func TestChecker_AllHealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusHealthy }, Critical: true},
			{Name: "mq", Check: func(_ context.Context) string { return StatusHealthy }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusHealthy {
		t.Fatalf("status = %q, want healthy", resp.Status)
	}
	if resp.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", resp.Version)
	}
}

func TestChecker_CriticalUnhealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusUnhealthy }, Critical: true},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusUnhealthy {
		t.Fatalf("status = %q, want unhealthy", resp.Status)
	}
}

func TestChecker_NonCriticalDegrades(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusHealthy }, Critical: true},
			{Name: "mq", Check: func(_ context.Context) string { return StatusConnecting }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusDegraded {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
}

func TestChecker_NoChecks(t *testing.T) {
	hc := &Checker{Version: "1.0.0"}
	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusHealthy {
		t.Fatalf("status = %q, want healthy", resp.Status)
	}
}

func TestChecker_CachesResults(t *testing.T) {
	callCount := 0
	hc := &Checker{
		Version:  "1.0.0",
		CacheTTL: 100 * time.Millisecond,
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string {
				callCount++
				return StatusHealthy
			}, Critical: true},
		},
	}

	hc.Evaluate(context.Background())
	hc.Evaluate(context.Background())

	if callCount != 1 {
		t.Errorf("check called %d times, want 1 (cached)", callCount)
	}
}

func TestChecker_CacheExpires(t *testing.T) {
	callCount := 0
	hc := &Checker{
		Version:  "1.0.0",
		CacheTTL: 1 * time.Millisecond,
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string {
				callCount++
				return StatusHealthy
			}, Critical: true},
		},
	}

	hc.Evaluate(context.Background())
	time.Sleep(5 * time.Millisecond)
	hc.Evaluate(context.Background())

	if callCount != 2 {
		t.Errorf("check called %d times, want 2 (cache expired)", callCount)
	}
}
