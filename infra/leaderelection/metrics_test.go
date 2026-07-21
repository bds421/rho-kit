package leaderelection

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestNewCallbackDrainMetricsReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	duration1, warns1 := NewCallbackDrainMetrics(reg)
	duration2, warns2 := NewCallbackDrainMetrics(reg)

	require.Same(t, duration1, duration2)
	require.Same(t, warns1, warns2)
}
