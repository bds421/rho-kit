// The kit's data module bundles every interface plus the small
// stdlib-only adapters that ship with them: actionlog (interface +
// memory), approval (interface + memory), budget (interface +
// memory), cache (interface + ristretto + tenant decorator),
// idempotency (interface + tenant decorator), lock (interface),
// queue (interface), ratelimit (interface + gcra + tokenbucket),
// stream (interface).
//
// Heavy adapters that pull pgx or redis SDKs stay split:
// actionlog/postgres, approval/postgres, budget/redis,
// cache/rediscache, idempotency/pgstore, idempotency/redisstore,
// lock/pgadvisory, lock/redislock, queue/redisqueue, ratelimit/redis,
// stream/redisstream.
module github.com/bds421/rho-kit/data/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.7.0
	github.com/bds421/rho-kit/observability/v2 v2.7.0
	github.com/dgraph-io/ristretto/v2 v2.4.2
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.2
	github.com/stretchr/testify v1.12.1
	golang.org/x/sync v0.22.0
	golang.org/x/time v0.15.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
