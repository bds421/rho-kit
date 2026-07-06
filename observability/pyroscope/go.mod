// Package github.com/bds421/rho-kit/observability/pyroscope/v2 —
// continuous-profiling adapter for Grafana Pyroscope. Separate module
// so the pyroscope-go runtime overhead (a sampling goroutine + an HTTP
// uploader) is pulled in only by services that opt in.
module github.com/bds421/rho-kit/observability/pyroscope/v2

go 1.26.2

require (
	github.com/grafana/pyroscope-go v1.4.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/grafana/pyroscope-go/godeltaprof v0.1.11 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
