// Package slo provides Service Level Objective (SLO) definitions, burn rate
// calculations, and health check integration for Prometheus-backed services.
//
// An SLO defines a target reliability level for a service, such as "99.9%
// of requests succeed" or "p99 latency under 500ms". The [Checker] evaluates
// SLOs against live Prometheus metrics and returns [SLOStatus] results that
// include current values, breach state, and burn rates.
//
// # Important: Internal Metrics Only
//
// This framework evaluates SLOs from the service's own in-process Prometheus
// counters and histograms. The success rate SLO ([TypeSuccessRate]) measures
// "of the requests I handled, what percentage succeeded?" — it does NOT
// measure true availability (uptime/reachability). If the service is down,
// it records nothing, so the success rate stays unchanged.
//
// True availability requires an external observer such as:
//   - Load balancer / ingress error rates
//   - Synthetic monitoring probes (e.g. Prometheus Blackbox Exporter)
//   - Sidecar or mesh-level metrics (e.g. Istio, Envoy)
//
// # Quick Start
//
//	slos := []slo.SLO{
//	    slo.ErrorRateSLO("api-errors", 0.001, 24*time.Hour),
//	    slo.SuccessRateSLO("api-success", 0.999, 24*time.Hour),
//	    slo.LatencySLO("api-latency", 0.99, 0.5, 24*time.Hour),
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
// The SLO evaluation results can be exposed as JSON via an HTTP handler.
// See the httpx package for the HTTP adapter (httpx/slohttp).
package slo
