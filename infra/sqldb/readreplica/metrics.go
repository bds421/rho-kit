package readreplica

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// gaugePoolLabel distinguishes the per-pool gauge series so multiple
// RoutingPools sharing a registerer (e.g. prometheus.DefaultRegisterer)
// keep independent gauge values instead of clobbering each other via Set
// and racing each other's Inc/Dec. Counters stay label-free: they are
// additively safe to share across pools.
const gaugePoolLabel = "pool"

type routingMetrics struct {
	primaryAcquires prometheus.Counter
	replicaAcquires prometheus.Counter
	replicaFallback prometheus.Counter
	healthyReplicas prometheus.Gauge
	replicaCount    prometheus.Gauge

	// Retained so Close can DeleteLabelValues and prevent unbounded
	// gauge series growth when RoutingPools are rebuilt.
	healthyVec *prometheus.GaugeVec
	countVec   *prometheus.GaugeVec
	instanceID string
}

// deleteSeries removes this pool's per-instance gauge children from the
// shared vectors. Idempotent; safe when metrics were never registered.
func (m *routingMetrics) deleteSeries() {
	if m == nil {
		return
	}
	if m.healthyVec != nil && m.instanceID != "" {
		_ = m.healthyVec.DeleteLabelValues(m.instanceID)
	}
	if m.countVec != nil && m.instanceID != "" {
		_ = m.countVec.DeleteLabelValues(m.instanceID)
	}
}

// newRoutingMetrics builds the routing metrics, registering (or adopting,
// on AlreadyRegisteredError) each collector on reg. instanceID identifies
// this RoutingPool so its gauge series are isolated from other pools that
// share the same registerer.
func newRoutingMetrics(reg prometheus.Registerer, instanceID string) (*routingMetrics, error) {
	primaryAcquires := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "sqldb",
		Subsystem: "readreplica",
		Name:      "primary_acquires_total",
		Help:      "Connections acquired from the primary pool (writes + non-read-only reads + fallbacks).",
	})
	replicaAcquires := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "sqldb",
		Subsystem: "readreplica",
		Name:      "replica_acquires_total",
		Help:      "Connections acquired from a healthy replica for a read-only request.",
	})
	replicaFallback := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "sqldb",
		Subsystem: "readreplica",
		Name:      "replica_fallback_total",
		Help:      "Read-only acquires that fell back to primary because no replica was healthy.",
	})
	healthyReplicas := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "sqldb",
		Subsystem: "readreplica",
		Name:      "replicas_healthy",
		Help:      "Number of replicas currently in rotation.",
	}, []string{gaugePoolLabel})
	replicaCount := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "sqldb",
		Subsystem: "readreplica",
		Name:      "replicas_total",
		Help:      "Total replicas configured (healthy + unhealthy).",
	}, []string{gaugePoolLabel})

	if reg != nil {
		var err error
		if primaryAcquires, err = registerCounter(reg, primaryAcquires); err != nil {
			return nil, err
		}
		if replicaAcquires, err = registerCounter(reg, replicaAcquires); err != nil {
			return nil, err
		}
		if replicaFallback, err = registerCounter(reg, replicaFallback); err != nil {
			return nil, err
		}
		if healthyReplicas, err = registerGaugeVec(reg, healthyReplicas); err != nil {
			return nil, err
		}
		if replicaCount, err = registerGaugeVec(reg, replicaCount); err != nil {
			return nil, err
		}
	}

	healthyGauge, err := healthyReplicas.GetMetricWithLabelValues(instanceID)
	if err != nil {
		return nil, err
	}
	countGauge, err := replicaCount.GetMetricWithLabelValues(instanceID)
	if err != nil {
		return nil, err
	}

	return &routingMetrics{
		primaryAcquires: primaryAcquires,
		replicaAcquires: replicaAcquires,
		replicaFallback: replicaFallback,
		healthyReplicas: healthyGauge,
		replicaCount:    countGauge,
		healthyVec:      healthyReplicas,
		countVec:        replicaCount,
		instanceID:      instanceID,
	}, nil
}

// registerCounter registers c, adopting the already-registered counter so
// multiple New() calls in the same process share additive state instead of
// fighting over registration.
func registerCounter(reg prometheus.Registerer, c prometheus.Counter) (prometheus.Counter, error) {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
				return existing, nil
			}
			return nil, errors.New("readreplica: metric name already registered with a different type")
		}
		return nil, err
	}
	return c, nil
}

// registerGaugeVec registers v, adopting the already-registered vector so
// every pool sharing a registerer pulls per-pool gauge children off the same
// vector rather than clobbering a single shared gauge.
func registerGaugeVec(reg prometheus.Registerer, v *prometheus.GaugeVec) (*prometheus.GaugeVec, error) {
	if err := reg.Register(v); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.GaugeVec); ok {
				return existing, nil
			}
			return nil, errors.New("readreplica: metric name already registered with a different type")
		}
		return nil, err
	}
	return v, nil
}
