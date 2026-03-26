package slo

import (
	"math"
	"time"
)

// CalculateBurnRate computes how fast the error budget is being consumed.
//
// A burn rate of 1.0 means the budget is consumed at exactly the expected rate
// over the SLO window. A burn rate >1 means the budget will be exhausted before
// the window ends. A burn rate of 0 means no budget is being consumed.
//
// The formula depends on the SLO type:
//   - ErrorRate: current / threshold (how many times over the budget)
//   - Availability: (1 - current) / (1 - threshold)
//   - Latency: current / threshold
//
// Returns 0 when no meaningful burn rate can be computed (NaN current, zero threshold).
func CalculateBurnRate(s SLO, current float64) float64 {
	if math.IsNaN(current) {
		return 0
	}

	switch s.Type {
	case TypeErrorRate:
		return burnRateErrorRate(s.Threshold, current)
	case TypeAvailability:
		return burnRateAvailability(s.Threshold, current)
	case TypeLatency:
		return burnRateLatency(s.Threshold, current)
	default:
		return 0
	}
}

// burnRateErrorRate computes burn rate for error rate SLOs.
// threshold is the max acceptable error rate (error budget).
// current is the observed error rate.
func burnRateErrorRate(threshold, current float64) float64 {
	if threshold <= 0 {
		return 0
	}
	return current / threshold
}

// burnRateAvailability computes burn rate for availability SLOs.
// threshold is the min acceptable availability (e.g. 0.999).
// current is the observed availability.
func burnRateAvailability(threshold, current float64) float64 {
	budget := 1 - threshold
	if budget <= 0 {
		return 0
	}
	consumed := 1 - current
	if consumed < 0 {
		return 0
	}
	return consumed / budget
}

// burnRateLatency computes burn rate for latency SLOs.
// threshold is the max acceptable latency.
// current is the observed latency.
func burnRateLatency(threshold, current float64) float64 {
	if threshold <= 0 {
		return 0
	}
	return current / threshold
}

// BurnRateAlert defines a multi-window burn rate alerting threshold.
// This follows Google's recommended multi-window, multi-burn-rate approach
// from the SRE Workbook.
type BurnRateAlert struct {
	// Severity is the alert severity (e.g. "critical", "warning").
	Severity string

	// LongWindow is the longer evaluation window (e.g. 1 hour).
	LongWindow time.Duration

	// ShortWindow is the shorter evaluation window (e.g. 5 minutes).
	ShortWindow time.Duration

	// BurnRateThreshold is the minimum burn rate to trigger (e.g. 14.4 for critical).
	BurnRateThreshold float64
}

// DefaultAlertThresholds returns the standard multi-window burn rate alert
// thresholds recommended by Google SRE. These correspond to:
//   - Critical (page): 14.4x burn in 1h, confirmed over 5m
//   - Warning (ticket): 6x burn in 6h, confirmed over 30m
//   - Info (log): 1x burn in 3d, confirmed over 6h
func DefaultAlertThresholds() []BurnRateAlert {
	return []BurnRateAlert{
		{Severity: "critical", LongWindow: 1 * time.Hour, ShortWindow: 5 * time.Minute, BurnRateThreshold: 14.4},
		{Severity: "warning", LongWindow: 6 * time.Hour, ShortWindow: 30 * time.Minute, BurnRateThreshold: 6},
		{Severity: "info", LongWindow: 72 * time.Hour, ShortWindow: 6 * time.Hour, BurnRateThreshold: 1},
	}
}

// EvaluateAlerts checks the given burn rate against alert thresholds and
// returns the highest-severity alert that fires, or nil if none fire.
// Only the burn rate threshold is checked -- multi-window evaluation requires
// separate short-window and long-window burn rate values from a time-series
// database, which is outside the scope of point-in-time evaluation.
//
// The alerts slice is not modified; the returned pointer is a copy.
func EvaluateAlerts(burnRate float64, alerts []BurnRateAlert) *BurnRateAlert {
	for i := range alerts {
		if burnRate >= alerts[i].BurnRateThreshold {
			result := alerts[i]
			return &result
		}
	}
	return nil
}
