// Package github.com/bds421/rho-kit/observability/pyroscope/v2 —
// continuous-profiling adapter for Grafana Pyroscope. Separate module
// so the pyroscope-go runtime overhead (a sampling goroutine + an HTTP
// uploader) is pulled in only by services that opt in.
module github.com/bds421/rho-kit/observability/pyroscope/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/grafana/pyroscope-go v1.2.0
	github.com/stretchr/testify v1.11.1
)

replace github.com/bds421/rho-kit/core/v2 => ../../core
