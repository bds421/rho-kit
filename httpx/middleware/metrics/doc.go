// Package metrics is the kit's legacy v1-era HTTP RED middleware.
//
// It emits `http_requests_total{method,route,status}`,
// `http_request_duration_seconds{method,route}`, and
// `http_requests_in_flight`. New services should prefer the v2 module
// [github.com/bds421/rho-kit/observability/v2/redmetrics], which
// separates errors into a `*_errors_total` counter and offers
// namespace/subsystem options for multi-service deployments where bare
// metric names would collide.
//
// This package exists for two reasons:
//
//   - Dashboard pinning. Existing dashboards and alerts were authored
//     against the v1 wire-form metric names (the bare http_requests_*
//     names with no subsystem prefix); renaming would silently void
//     them. The package is preserved without breaking changes so legacy
//     services keep working through the v2.0.0 transition.
//   - Backstop for embedded handlers. Test fixtures and small internal
//     tools that don't run the full Builder stack still need a single-
//     line drop-in.
//
// Both packages may coexist in one binary so long as they register
// against different Prometheus registries (or different metric names).
package metrics
