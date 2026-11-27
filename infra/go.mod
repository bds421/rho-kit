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

go 1.27.4

require (
	github.com/bds421/rho-kit/core/v2 v2.50.0
	github.com/bds421/rho-kit/io/v2 v2.50.0
	github.com/bds421/rho-kit/observability/v2 v2.50.0
	github.com/bds421/rho-kit/runtime/v2 v2.50.0
	github.com/gabriel-vasile/mimetype v1.4.16
	github.com/google/uuid v1.6.0
	github.com/pressly/goose/v3 v3.30.1
	github.com/prometheus/client_golang v1.25.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/stretchr/testify v1.12.1
	github.com/tink-crypto/tink-go/v2 v2.9.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.74.0 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

retract v2.9.0 // Tagged from branches that were never merged; use v2.10.0.
