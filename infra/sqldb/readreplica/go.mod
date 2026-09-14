// Package github.com/bds421/rho-kit/infra/sqldb/readreplica/v2
// routes Postgres reads to replicas while keeping writes on the primary
// pool. Lives in its own module so consumers of infra/sqldb/pgx that
// don't need replica routing don't pull this code (and so future
// additional backends — e.g. a MySQL routing pool — can land beside
// it without changing the pgx adapter).
module github.com/bds421/rho-kit/infra/sqldb/readreplica/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.7.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.3
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
