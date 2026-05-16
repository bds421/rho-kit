module github.com/bds421/rho-kit/examples/saga-coordinator/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/bds421/rho-kit/httpx/v2 v2.0.0
	github.com/bds421/rho-kit/runtime/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/observability/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/resilience/v2 v2.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.68.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../core

replace github.com/bds421/rho-kit/observability/v2 => ../../observability

replace github.com/bds421/rho-kit/resilience/v2 => ../../resilience

replace github.com/bds421/rho-kit/data/v2 => ../../data

replace github.com/bds421/rho-kit/httpx/v2 => ../../httpx

replace github.com/bds421/rho-kit/runtime/v2 => ../../runtime
