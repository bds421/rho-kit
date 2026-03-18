package promutil

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// RegisterCollector registers a Prometheus collector, silently reusing an
// existing collector on [prometheus.AlreadyRegisteredError]. Panics on other
// registration errors (e.g. metric name conflicts).
//
// This is the canonical registration helper for the kit workspace. All
// sub-packages that register Prometheus collectors should use this function
// to avoid duplicating registration logic.
func RegisterCollector(reg prometheus.Registerer, c prometheus.Collector) {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
	}
}
