package slo

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCalculateBurnRate_ErrorRate(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		current   float64
		want      float64
	}{
		{"zero errors", 0.01, 0.0, 0.0},
		{"at budget", 0.01, 0.01, 1.0},
		{"double budget", 0.01, 0.02, 2.0},
		{"half budget", 0.01, 0.005, 0.5},
		{"zero threshold", 0.0, 0.01, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SLO{Type: TypeErrorRate, Threshold: tt.threshold}
			got := CalculateBurnRate(s, tt.current)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestCalculateBurnRate_SuccessRate(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		current   float64
		want      float64
	}{
		{"perfect success rate", 0.999, 1.0, 0.0},
		{"at budget", 0.999, 0.999, 1.0},
		{"double budget consumed", 0.999, 0.998, 2.0},
		{"threshold 1.0 (zero budget)", 1.0, 0.999, 0.0},
		{"better than threshold", 0.999, 1.001, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SLO{Type: TypeSuccessRate, Threshold: tt.threshold}
			got := CalculateBurnRate(s, tt.current)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestCalculateBurnRate_Latency(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		current   float64
		want      float64
	}{
		{"well within", 0.5, 0.1, 0.2},
		{"at threshold", 0.5, 0.5, 1.0},
		{"over threshold", 0.5, 1.0, 2.0},
		{"zero threshold", 0.0, 0.5, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SLO{Type: TypeLatency, Threshold: tt.threshold}
			got := CalculateBurnRate(s, tt.current)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestCalculateBurnRate_NaN(t *testing.T) {
	s := SLO{Type: TypeErrorRate, Threshold: 0.01}
	got := CalculateBurnRate(s, math.NaN())
	assert.Equal(t, 0.0, got)
}

func TestCalculateBurnRate_UnknownType(t *testing.T) {
	s := SLO{Type: SLOType("bogus"), Threshold: 0.5}
	got := CalculateBurnRate(s, 0.3)
	assert.Equal(t, 0.0, got)
}

func TestDefaultAlertThresholds(t *testing.T) {
	alerts := DefaultAlertThresholds()
	assert.Len(t, alerts, 3)
	assert.Equal(t, "critical", alerts[0].Severity)
	assert.Equal(t, 1*time.Hour, alerts[0].LongWindow)
	assert.Equal(t, 5*time.Minute, alerts[0].ShortWindow)
	assert.Equal(t, "warning", alerts[1].Severity)
	assert.Equal(t, 6*time.Hour, alerts[1].LongWindow)
	assert.Equal(t, 30*time.Minute, alerts[1].ShortWindow)
	assert.Equal(t, "info", alerts[2].Severity)
	assert.Equal(t, 72*time.Hour, alerts[2].LongWindow)
	assert.Equal(t, 6*time.Hour, alerts[2].ShortWindow)
}

func TestEvaluateAlerts_Critical(t *testing.T) {
	alerts := DefaultAlertThresholds()
	result := EvaluateAlerts(15.0, alerts)

	assert.NotNil(t, result)
	assert.Equal(t, "critical", result.Severity)
}

func TestEvaluateAlerts_Warning(t *testing.T) {
	alerts := DefaultAlertThresholds()
	result := EvaluateAlerts(7.0, alerts)

	assert.NotNil(t, result)
	assert.Equal(t, "warning", result.Severity)
}

func TestEvaluateAlerts_Info(t *testing.T) {
	alerts := DefaultAlertThresholds()
	result := EvaluateAlerts(1.5, alerts)

	assert.NotNil(t, result)
	assert.Equal(t, "info", result.Severity)
}

func TestEvaluateAlerts_None(t *testing.T) {
	alerts := DefaultAlertThresholds()
	result := EvaluateAlerts(0.5, alerts)

	assert.Nil(t, result)
}

func TestEvaluateAlerts_EmptySlice(t *testing.T) {
	result := EvaluateAlerts(100.0, nil)
	assert.Nil(t, result)
}

func TestEvaluateAlerts_DoesNotMutateInput(t *testing.T) {
	alerts := DefaultAlertThresholds()
	original := make([]BurnRateAlert, len(alerts))
	copy(original, alerts)

	result := EvaluateAlerts(15.0, alerts)
	assert.NotNil(t, result)

	// Mutating the result should not affect the original slice.
	result.Severity = "mutated"
	assert.Equal(t, "critical", alerts[0].Severity)
	assert.Equal(t, original, alerts)
}
