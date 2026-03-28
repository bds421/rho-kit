package outbox_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/outbox"
)

func TestNewMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := outbox.NewMetrics(outbox.WithRegisterer(reg))
	require.NotNil(t, m)

	families, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	require.True(t, names["outbox_pending_count"], "missing outbox_pending_count")
	require.True(t, names["outbox_relay_latency_seconds"], "missing outbox_relay_latency_seconds")
	require.True(t, names["outbox_published_total"], "missing outbox_published_total")
	require.True(t, names["outbox_errors_total"], "missing outbox_errors_total")
}

func TestNewMetrics_DefaultRegisterer(t *testing.T) {
	m := outbox.NewMetrics()
	require.NotNil(t, m)
}

func TestNewMetrics_DoubleRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := outbox.NewMetrics(outbox.WithRegisterer(reg))
	require.NotNil(t, m1)

	// Second registration should not panic (promutil.RegisterCollector handles it).
	m2 := outbox.NewMetrics(outbox.WithRegisterer(reg))
	require.NotNil(t, m2)
}
