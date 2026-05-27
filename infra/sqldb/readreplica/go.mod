// Package github.com/bds421/rho-kit/infra/sqldb/readreplica/v2
// routes Postgres reads to replicas while keeping writes on the primary
// pool. Lives in its own module so consumers of infra/sqldb/pgx that
// don't need replica routing don't pull this code (and so future
// additional backends — e.g. a MySQL routing pool — can land beside
// it without changing the pgx adapter).
module github.com/bds421/rho-kit/infra/sqldb/readreplica/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/prometheus/client_golang v1.23.2
	github.com/stretchr/testify v1.11.1
)
