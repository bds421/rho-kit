module github.com/bds421/rho-kit/app/amqp/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/app/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/observability/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)

replace github.com/bds421/rho-kit/app/v2 => ../

replace github.com/bds421/rho-kit/authz/v2 => ../../authz

replace github.com/bds421/rho-kit/core/v2 => ../../core

replace github.com/bds421/rho-kit/crypto/v2 => ../../crypto

replace github.com/bds421/rho-kit/data/v2 => ../../data

replace github.com/bds421/rho-kit/flags/v2 => ../../flags

replace github.com/bds421/rho-kit/grpcx/v2 => ../../grpcx

replace github.com/bds421/rho-kit/httpx/v2 => ../../httpx

replace github.com/bds421/rho-kit/infra/v2 => ../../infra

replace github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 => ../../infra/messaging/amqpbackend

replace github.com/bds421/rho-kit/infra/messaging/natsbackend/v2 => ../../infra/messaging/natsbackend

replace github.com/bds421/rho-kit/infra/redis/v2 => ../../infra/redis

replace github.com/bds421/rho-kit/infra/sqldb/pgx/v2 => ../../infra/sqldb/pgx

replace github.com/bds421/rho-kit/io/v2 => ../../io

replace github.com/bds421/rho-kit/observability/v2 => ../../observability

replace github.com/bds421/rho-kit/resilience/v2 => ../../resilience

replace github.com/bds421/rho-kit/runtime/v2 => ../../runtime

replace github.com/bds421/rho-kit/security/v2 => ../../security
