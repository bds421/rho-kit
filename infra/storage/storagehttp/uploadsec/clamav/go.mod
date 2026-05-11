module github.com/bds421/rho-kit/infra/storage/storagehttp/uploadsec/clamav/v2

go 1.26.2

require github.com/bds421/rho-kit/infra/v2 v2.0.0

require (
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../../../core

replace github.com/bds421/rho-kit/crypto/v2 => ../../../../../crypto

replace github.com/bds421/rho-kit/infra/v2 => ../../../..

replace github.com/bds421/rho-kit/io/v2 => ../../../../../io

replace github.com/bds421/rho-kit/observability/v2 => ../../../../../observability

replace github.com/bds421/rho-kit/resilience/v2 => ../../../../../resilience
