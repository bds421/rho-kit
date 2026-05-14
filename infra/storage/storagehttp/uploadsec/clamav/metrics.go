package clamav

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/infra/v2/storage/storagehttp/uploadsec"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Outcome label values for clamav_scans_total. They form a closed set so
// dashboards can rely on consistent labels across scanners.
const (
	scanOutcomeClean    = "clean"
	scanOutcomeInfected = "infected"
	scanOutcomeError    = "error"
)

// Metrics holds Prometheus collectors for clamav scan observability.
//
// Two metrics are exported:
//
//   - clamav_scan_duration_seconds{validator}  histogram of scan latency.
//     The validator label is set by WithMetricsValidatorName so operators
//     can split when multiple scanners run side-by-side (e.g. clamav +
//     a YARA validator); default is "clamav".
//   - clamav_scans_total{validator,outcome}    count of completed scans
//     partitioned by outcome: clean, infected, error.
//
// The metric naming follows the kit-wide <subsystem>_<verb>_<unit>
// convention so all rho-kit dashboards can match a single naming scheme.
// Metrics are opt-in via WithMetrics.
type Metrics struct {
	scanDuration *prometheus.HistogramVec
	scansTotal   *prometheus.CounterVec
}

// MetricsOption configures the clamav metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer. Unset defaults to
// [prometheus.DefaultRegisterer]; passing nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("clamav: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers clamav metrics. Pass [WithRegisterer]
// for a non-default registry. MustRegisterOrGet folds re-registration
// against the same Registerer into a single metric set so tests that
// build many scanners against one registry behave deterministically.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("clamav: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		scanDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "clamav",
				Name:      "scan_duration_seconds",
				Help:      "Duration of a clamav INSTREAM scan in seconds.",
				// Buckets cover the typical clamd round-trip range: a
				// few ms for small uploads up to ~30s for the default
				// scanTimeout. Wider than DefBuckets at the long tail
				// because operators care about scans approaching the
				// timeout.
				Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
			},
			[]string{"validator"},
		),
		scansTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "clamav",
				Name:      "scans_total",
				Help:      "Total clamav scans by outcome (clean, infected, error).",
			},
			[]string{"validator", "outcome"},
		),
	}

	m.scanDuration = promutil.MustRegisterOrGet(reg, m.scanDuration)
	m.scansTotal = promutil.MustRegisterOrGet(reg, m.scansTotal)
	return m
}

// observeScan records a single scan outcome and latency. err is the
// scanner's verdict: nil → clean, ErrMalwareDetected → infected, anything
// else (ErrScannerUnavailable, protocol errors, dial failures) → error.
//
// Splitting infected vs error is essential — a sustained "error" rate is
// a clamd outage that fails closed, while a sustained "infected" rate
// could be a coordinated upload attack. They demand different on-call
// responses and must be on separate alerts.
func (m *Metrics) observeScan(validator string, started time.Time, err error) {
	if m == nil {
		return
	}
	m.scanDuration.WithLabelValues(validator).Observe(time.Since(started).Seconds())
	m.scansTotal.WithLabelValues(validator, classifyScanOutcome(err)).Inc()
}

// classifyScanOutcome maps a Scan return value to one of the closed-set
// outcome labels. Kept package-private so the label set cannot drift via
// caller-supplied strings.
func classifyScanOutcome(err error) string {
	switch {
	case err == nil:
		return scanOutcomeClean
	case errors.Is(err, uploadsec.ErrMalwareDetected):
		return scanOutcomeInfected
	default:
		return scanOutcomeError
	}
}
