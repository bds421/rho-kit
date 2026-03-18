package promutil

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
)

func TestRegisterCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_counter",
		Help: "A test counter.",
	})

	// First registration succeeds.
	RegisterCollector(reg, c)

	// Duplicate registration does not panic.
	assert.NotPanics(t, func() {
		RegisterCollector(reg, c)
	})
}

func TestRegisterCollector_PanicsOnConflict(t *testing.T) {
	reg := prometheus.NewRegistry()
	c1 := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "conflict_counter",
		Help: "First.",
	})
	c2 := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "conflict_counter",
		Help: "Second with same name but different type.",
	})

	RegisterCollector(reg, c1)

	assert.Panics(t, func() {
		RegisterCollector(reg, c2)
	})
}
