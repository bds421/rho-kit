// Package openfga implements [authz.Decider] against OpenFGA — the
// CNCF-incubating ReBAC engine that scales to millions of tuples
// with millisecond decisions. v2 chose OpenFGA as the reference
// engine adapter because it has the strongest combination of
// scalability, language SDK quality, and CNCF governance.
//
// Heavy: pulls the OpenFGA Go SDK + grpc. Stays in its own module
// so consumers using the in-memory adapter (or a different engine
// later) don't pay the SDK cost.
module github.com/bds421/rho-kit/authz/openfga

go 1.26

require (
	github.com/bds421/rho-kit/authz v0.0.0
	github.com/openfga/go-sdk v0.7.4
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.39.0 // indirect
	go.opentelemetry.io/otel/metric v1.39.0 // indirect
	go.opentelemetry.io/otel/trace v1.39.0 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	go.uber.org/multierr v1.9.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
)

replace github.com/bds421/rho-kit/authz => ../

replace github.com/bds421/rho-kit/core => ../../core
