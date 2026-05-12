module github.com/bds421/rho-kit/infra/storage/azurebackend/v2

go 1.26.2

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.4
	github.com/bds421/rho-kit/observability/v2 v2.0.0
	github.com/prometheus/client_golang v1.23.2
	go.opentelemetry.io/otel v1.42.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.19.2 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.20.0
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.42.0 // indirect
	go.opentelemetry.io/otel/trace v1.42.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/infra/v2 => ../../../infra

replace github.com/bds421/rho-kit/io/v2 => ../../../io

replace github.com/bds421/rho-kit/observability/v2 => ../../../observability
