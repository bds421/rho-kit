// The kit's runtime module bundles long-running orchestration
// primitives: batchworker, concurrency helpers, cron, eventbus,
// lifecycle. Every consumer of app.Builder pulls these transitively
// so consolidation reduces module sprawl without changing dep
// footprint. See AGENTS.md "Module shape" for the consolidation map.
module github.com/bds421/rho-kit/runtime/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.1
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
	github.com/robfig/cron/v3 v3.0.1
	github.com/stretchr/testify v1.11.1
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/trace v1.44.0
	golang.org/x/sync v0.20.0
)

require (
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
)

require (
	github.com/bds421/rho-kit/observability/v2 v2.0.1
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/sys v0.45.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
