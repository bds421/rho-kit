package secrets

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

type cacheMetrics struct {
	hits           prometheus.Counter
	misses         prometheus.Counter
	refreshes      prometheus.Counter
	refreshErrors  prometheus.Counter
	staleFallbacks prometheus.Counter
	staleExceeded  prometheus.Counter
}

func newCacheMetrics(reg prometheus.Registerer) (*cacheMetrics, error) {
	m := &cacheMetrics{
		hits:           cacheCounter("hits_total", "Cache hits served without invoking the backend Loader."),
		misses:         cacheCounter("misses_total", "Cache misses that triggered a foreground Loader fetch."),
		refreshes:      cacheCounter("background_refreshes_total", "Background stale-while-revalidate refresh fetches."),
		refreshErrors:  cacheCounter("background_refresh_errors_total", "Background refreshes that returned an error."),
		staleFallbacks: cacheCounter("stale_fallbacks_total", "Get calls served from a stale cached value because the Loader was unavailable."),
		staleExceeded:  cacheCounter("stale_exceeded_total", "Get calls where the cached value was older than WithCacheMaxStale; surfaced the error."),
	}
	if reg == nil {
		return m, nil
	}
	counters := []*prometheus.Counter{
		&m.hits, &m.misses, &m.refreshes, &m.refreshErrors,
		&m.staleFallbacks, &m.staleExceeded,
	}
	for _, c := range counters {
		if err := registerCounter(reg, c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func cacheCounter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "secrets",
		Subsystem: "cache",
		Name:      name,
		Help:      help,
	})
}

// registerCounter registers *c, or points it at the counter already
// registered under the same name.
func registerCounter(reg prometheus.Registerer, c *prometheus.Counter) error {
	err := reg.Register(*c)
	if err == nil {
		return nil
	}
	var are prometheus.AlreadyRegisteredError
	if !errors.As(err, &are) {
		return err
	}
	if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
		*c = existing
	}
	return nil
}
