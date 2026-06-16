package secrets

import (
	"time"

	dto "github.com/prometheus/client_model/go"
)

// This file is a white-box (package secrets) test seam. It exposes the
// otherwise-unexported clock injection (cfg.now) and the internal cache
// metric counters so black-box behavioral tests can drive the
// stale-while-revalidate background-refresh path deterministically
// without relying on real-time sleeps. Nothing here widens the public
// API: identifiers are exported only within _test builds.

// SetClock overrides the CachedLoader's internal clock. It must be called
// before any concurrent Get so the swap races nothing.
func (c *CachedLoader) SetClock(now func() time.Time) {
	c.cfg.now = now
}

// counterValue reads the current value of a prometheus counter via the
// dto.Metric write path (already a module dependency), avoiding the
// prometheus testutil package which would pull a new dependency.
func counterValue(metric interface {
	Write(*dto.Metric) error
}) float64 {
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		return -1
	}
	if m.Counter == nil || m.Counter.Value == nil {
		return 0
	}
	return *m.Counter.Value
}

// MetricRefreshes returns the background_refreshes_total counter value.
func (c *CachedLoader) MetricRefreshes() float64 { return counterValue(c.metrics.refreshes) }

// MetricRefreshErrors returns the background_refresh_errors_total value.
func (c *CachedLoader) MetricRefreshErrors() float64 { return counterValue(c.metrics.refreshErrors) }

// MetricStaleFallbacks returns the stale_fallbacks_total counter value.
func (c *CachedLoader) MetricStaleFallbacks() float64 { return counterValue(c.metrics.staleFallbacks) }

// MetricHits returns the hits_total counter value.
func (c *CachedLoader) MetricHits() float64 { return counterValue(c.metrics.hits) }
