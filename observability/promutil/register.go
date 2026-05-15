package promutil

import (
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// RegisterCollector registers a Prometheus collector, silently accepting an
// already-registered equivalent collector on
// [prometheus.AlreadyRegisteredError]. Panics on other registration errors
// (e.g. metric name conflicts).
//
// This is the canonical registration helper for the kit workspace. All
// sub-packages that register Prometheus collectors should use this function
// to avoid duplicating registration logic.
//
// Use this only for fire-and-forget collectors where the caller does not keep a
// metric handle. Constructors that return counters/gauges/histograms must use
// [MustRegisterOrGet] so duplicate construction records into the registered
// collector instead of an unregistered local value.
func RegisterCollector(reg prometheus.Registerer, c prometheus.Collector) {
	if _, err := Register(reg, c); err != nil {
		panic("promutil: RegisterCollector: metric registration failed")
	}
}

// MustRegisterOrGet registers c and returns the collector that is live in reg.
// If an equivalent collector was already registered, the existing collector is
// returned. Panics on registration conflicts or if the existing collector has
// an unexpected type.
//
// Use this in constructors that return metric handles. Calling
// RegisterCollector and then keeping c is wrong on duplicate registration: c is
// not registered, so later observations disappear from the registry.
func MustRegisterOrGet[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	registered, err := Register(reg, c)
	if err != nil {
		panic("promutil: metric registration failed")
	}
	typed, ok := registered.(T)
	if !ok {
		panic("promutil: registered collector type mismatch")
	}
	return typed
}

// Register registers a Prometheus collector and returns the collector
// that is live in reg paired with a registration error.
//
//   - On success the returned collector is c and err is nil.
//   - On [prometheus.AlreadyRegisteredError] the existing collector is
//     logged at debug level and returned in place of c; err is nil.
//     Callers MUST observe through the returned collector — observations
//     on c would record into an unregistered local.
//   - On any other registration error (name shape conflict, label
//     mismatch with a different collector type) the first result is nil
//     and err is returned unchanged so callers can decide whether to
//     panic, retry, or abort. There is no third "ok" boolean; the
//     reuse path is signalled by `err == nil && returned != c`.
//
// Use [MustRegisterOrGet] in constructors that need the result typed,
// or [RegisterCollector] for fire-and-forget collectors that do not
// keep a handle.
func Register(reg prometheus.Registerer, c prometheus.Collector) (prometheus.Collector, error) {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			slog.Default().Debug("promutil: collector already registered, reusing existing",
				"collector_type", typeName(c),
			)
			return are.ExistingCollector, nil
		}
		return nil, err
	}
	return c, nil
}

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	type named interface{ Name() string }
	if n, ok := v.(named); ok {
		return n.Name()
	}
	return ""
}
