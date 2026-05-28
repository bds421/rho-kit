// The kit's infra module bundles every infrastructure interface plus
// the small stdlib-only helpers that ship with them: sqldb (config,
// pool, errors, escape, migrate), messaging (interface +
// schema helpers), storage (interface + storagehttp + storagehttp/
// uploadsec), leaderelection (interface), outbox (interface +
// relay).
//
// Heavy adapters that pull SDKs stay split: sqldb/pgx, messaging/
// amqpbackend, messaging/natsbackend, messaging/redisbackend, redis
// (go-redis), storage/{s3,gcs,azure,sftp}backend, leaderelection/
// {pgadvisory,redislock}, storagehttp/uploadsec/clamav, sqldb/dbtest,
// storage/storagetest. See AGENTS.md "Module shape" for the full split.
module github.com/bds421/rho-kit/infra/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.1
	github.com/bds421/rho-kit/crypto/v2 v2.0.1
	github.com/bds421/rho-kit/observability/v2 v2.0.1
	github.com/bds421/rho-kit/resilience/v2 v2.0.1
	github.com/gabriel-vasile/mimetype v1.4.13
	github.com/google/uuid v1.6.0
	github.com/pressly/goose/v3 v3.27.1
	github.com/prometheus/client_golang v1.23.2
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/tink-crypto/tink-go/v2 v2.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
)

require (
	github.com/bds421/rho-kit/io/v2 v2.0.1
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
