package metricscontract

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewReusesSharedCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	duration1, warns1 := New(reg)
	duration2, warns2 := New(reg)
	if duration1 != duration2 || warns1 != warns2 {
		t.Fatal("shared collector construction must reuse registered collectors")
	}
}
