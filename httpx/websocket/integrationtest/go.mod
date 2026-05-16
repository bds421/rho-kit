// Package integrationtest holds the cross-network integration tests for
// httpx/websocket. The production module does not pull a test
// listener; this submodule exists to keep the dependency closure of
// the public API minimal.
module github.com/bds421/rho-kit/httpx/websocket/integrationtest/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/httpx/websocket/v2 v2.0.0
	github.com/coder/websocket v1.8.14
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/observability/v2 v2.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sys v0.44.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/httpx/v2 => ../../

replace github.com/bds421/rho-kit/httpx/websocket/v2 => ../

replace github.com/bds421/rho-kit/observability/v2 => ../../../observability

replace github.com/bds421/rho-kit/authz/v2 => ../../../authz

replace github.com/bds421/rho-kit/crypto/v2 => ../../../crypto

replace github.com/bds421/rho-kit/data/v2 => ../../../data

replace github.com/bds421/rho-kit/resilience/v2 => ../../../resilience

replace github.com/bds421/rho-kit/security/v2 => ../../../security
