// Package riverqueue is the v2 default for durable Postgres-backed
// jobs. It wraps github.com/riverqueue/river — a Go-native job queue
// that uses your existing Postgres for storage, no extra
// infrastructure.
//
// When to use this vs. the Redis queue: River for durability ("must
// not lose this job"), Redis for lightweight in-flight coordination
// ("dedupe a webhook for 30 seconds"). v2 demoted Redis queue from
// the durable-job default; new services should choose River unless
// they have a specific lightweight reason to pick the alternative.
//
// Heavy: pulls riverqueue/river + pgx + driver. Stays in its own
// module so consumers that don't need durable jobs don't pull the
// SDK transitively.
module github.com/bds421/rho-kit/data/queue/riverqueue/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/riverqueue/river v0.37.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.37.0
	github.com/riverqueue/river/rivertype v0.37.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/riverqueue/river/riverdriver v0.37.0 // indirect
	github.com/riverqueue/river/rivershared v0.37.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.uber.org/goleak v1.3.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
