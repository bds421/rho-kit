// Package slo provides Service Level Objective (SLO) definitions, burn rate
// calculations, and health check integration for Prometheus-backed services.
//
// An SLO defines a target reliability level for a service, such as "99.9%
// of requests succeed" or "p99 latency under 500ms". The [Checker] evaluates
// SLOs against live Prometheus metrics and returns [SLOStatus] results that
// include current values, breach state, and burn rates.
//
// # Quick Start
//
//	slos := []slo.SLO{
//	    slo.HTTPErrorRateSLO("api-errors", 0.001, 24*time.Hour),
//	    slo.HTTPLatencySLO("api-latency", 0.99, 0.5, 24*time.Hour),
//	}
//	checker := slo.NewChecker(prometheus.DefaultGatherer, slos...)
//	statuses := checker.Evaluate()
//
// # Health Integration
//
// Use [Checker.DependencyCheckFunc] to produce a function compatible with
// health.DependencyCheck that reports degraded status when any SLO is breached.
//
// # HTTP Endpoint
//
// Use [Handler] to expose SLO status as a JSON endpoint.
package slo
