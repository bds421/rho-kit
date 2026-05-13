module github.com/bds421/rho-kit/infra/storage/storagehttp/uploadsec/clamav/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/observability/v2 v2.0.0
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
)

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.19.2 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sys v0.43.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../../../core

replace github.com/bds421/rho-kit/crypto/v2 => ../../../../../crypto

replace github.com/bds421/rho-kit/infra/v2 => ../../../..

replace github.com/bds421/rho-kit/io/v2 => ../../../../../io

replace github.com/bds421/rho-kit/observability/v2 => ../../../../../observability

replace github.com/bds421/rho-kit/resilience/v2 => ../../../../../resilience
