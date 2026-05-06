package promutil

import (
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// RegisterCollector registers a Prometheus collector, silently reusing an
// existing collector on [prometheus.AlreadyRegisteredError]. Panics on other
// registration errors (e.g. metric name conflicts).
//
// This is the canonical registration helper for the kit workspace. All
// sub-packages that register Prometheus collectors should use this function
// to avoid duplicating registration logic.
//
// Prefer [Register] when callers need to react to the AlreadyRegistered case
// (e.g. swap in the existing collector, log a debug line). RegisterCollector
// is panic-on-conflict by design — most kit modules construct fresh
// collectors during process startup, where reuse-on-startup is the only
// acceptable shared-fate outcome.
func RegisterCollector(reg prometheus.Registerer, c prometheus.Collector) {
	if _, err := Register(reg, c); err != nil {
		panic(err)
	}
}

// Register registers a Prometheus collector and reports whether an existing
// equivalent collector was reused (returned in place of c). On conflict the
// existing collector is logged at debug level and returned via the first
// result; ok is false and err is nil. On other registration errors err is
// returned unchanged so callers can decide whether to panic, retry, or
// abort.
//
// The returned collector is the one that is actually live in reg: if an
// equivalent was already registered, that one wins (Prometheus client
// semantics); otherwise it is c.
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
