// Package github.com/bds421/rho-kit/observability/pyroscope/v2 —
// continuous-profiling adapter for Grafana Pyroscope. Separate module
// so the pyroscope-go runtime overhead (a sampling goroutine + an HTTP
// uploader) is pulled in only by services that opt in.
module github.com/bds421/rho-kit/observability/pyroscope/v2

go 1.26.2

require (
	github.com/grafana/pyroscope-go v1.4.2
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/grafana/pyroscope-go/godeltaprof v0.1.11 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
