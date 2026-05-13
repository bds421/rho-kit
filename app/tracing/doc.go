// Package tracing wires an OpenTelemetry TracerProvider into the Builder via
// the v2.0.0 lazy-adapter Module API. Importing this package is the explicit
// declaration that the service emits OTel traces; services that don't need
// tracing can omit it and avoid pulling the OTLP gRPC exporter into their
// binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/tracing/v2"
//	    tracingcfg "github.com/bds421/rho-kit/observability/v2/tracing"
//	)
//
//	app.New("svc", "v1", base).
//	    With(tracing.Module(tracingcfg.Config{Endpoint: cfg.OTLPEndpoint})).
//	    Router(...).
//	    Run()
//
// The Module also satisfies app.TracingProvider so the kit's auto-wired
// HTTP client picks up tracing instrumentation when the module is present.
//
// Failure semantics: the Module never aborts startup on a tracing init
// error. Failures register a degraded health check so the operator sees
// the regression without the service refusing to serve traffic.
package tracing
