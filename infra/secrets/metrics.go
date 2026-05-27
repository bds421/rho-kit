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
		hits: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "hits_total",
			Help:      "Cache hits served without invoking the backend Loader.",
		}),
		misses: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "misses_total",
			Help:      "Cache misses that triggered a foreground Loader fetch.",
		}),
		refreshes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "background_refreshes_total",
			Help:      "Background stale-while-revalidate refresh fetches.",
		}),
		refreshErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "background_refresh_errors_total",
			Help:      "Background refreshes that returned an error.",
		}),
		staleFallbacks: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "stale_fallbacks_total",
			Help:      "Get calls served from a stale cached value because the Loader was unavailable.",
		}),
		staleExceeded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "secrets",
			Subsystem: "cache",
			Name:      "stale_exceeded_total",
			Help:      "Get calls where the cached value was older than WithCacheMaxStale; surfaced the error.",
		}),
	}
	if reg == nil {
		return m, nil
	}
	collectors := []prometheus.Counter{
		m.hits, m.misses, m.refreshes, m.refreshErrors,
		m.staleFallbacks, m.staleExceeded,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
					switch c {
					case m.hits:
						m.hits = existing
					case m.misses:
						m.misses = existing
					case m.refreshes:
						m.refreshes = existing
					case m.refreshErrors:
						m.refreshErrors = existing
					case m.staleFallbacks:
						m.staleFallbacks = existing
					case m.staleExceeded:
						m.staleExceeded = existing
					}
				}
				continue
			}
			return nil, err
		}
	}
	return m, nil
}
