// Package openfga implements [authz.Decider] against OpenFGA — the
// CNCF-incubating ReBAC engine that scales to millions of tuples
// with millisecond decisions. v2 chose OpenFGA as the reference
// engine adapter because it has the strongest combination of
// scalability, language SDK quality, and CNCF governance.
//
// Heavy: pulls the OpenFGA Go SDK + grpc. Stays in its own module
// so consumers using the in-memory adapter (or a different engine
// later) don't pay the SDK cost.
module github.com/bds421/rho-kit/authz/openfga/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/authz/v2 v2.2.0
	github.com/bds421/rho-kit/core/v2 v2.2.0
	github.com/openfga/go-sdk v0.8.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
)
