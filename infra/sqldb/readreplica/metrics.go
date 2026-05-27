package readreplica

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

type routingMetrics struct {
	primaryAcquires prometheus.Counter
	replicaAcquires prometheus.Counter
	replicaFallback prometheus.Counter
	healthyReplicas prometheus.Gauge
	replicaCount    prometheus.Gauge
}

func newRoutingMetrics(reg prometheus.Registerer) (*routingMetrics, error) {
	m := &routingMetrics{
		primaryAcquires: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sqldb",
			Subsystem: "readreplica",
			Name:      "primary_acquires_total",
			Help:      "Connections acquired from the primary pool (writes + non-read-only reads + fallbacks).",
		}),
		replicaAcquires: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sqldb",
			Subsystem: "readreplica",
			Name:      "replica_acquires_total",
			Help:      "Connections acquired from a healthy replica for a read-only request.",
		}),
		replicaFallback: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sqldb",
			Subsystem: "readreplica",
			Name:      "replica_fallback_total",
			Help:      "Read-only acquires that fell back to primary because no replica was healthy.",
		}),
		healthyReplicas: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sqldb",
			Subsystem: "readreplica",
			Name:      "replicas_healthy",
			Help:      "Number of replicas currently in rotation.",
		}),
		replicaCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sqldb",
			Subsystem: "readreplica",
			Name:      "replicas_total",
			Help:      "Total replicas configured (healthy + unhealthy).",
		}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{
		m.primaryAcquires, m.replicaAcquires, m.replicaFallback,
		m.healthyReplicas, m.replicaCount,
	} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				// Adopt the already-registered collector so multiple
				// New() in the same process share state instead of
				// fighting over registration.
				if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
					switch c {
					case m.primaryAcquires:
						m.primaryAcquires = existing
					case m.replicaAcquires:
						m.replicaAcquires = existing
					case m.replicaFallback:
						m.replicaFallback = existing
					}
				}
				if existing, ok := are.ExistingCollector.(prometheus.Gauge); ok {
					switch c {
					case m.healthyReplicas:
						m.healthyReplicas = existing
					case m.replicaCount:
						m.replicaCount = existing
					}
				}
				continue
			}
			return nil, err
		}
	}
	return m, nil
}
