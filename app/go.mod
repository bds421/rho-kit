module github.com/bds421/rho-kit/app/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/authz/v2 v2.0.0
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/crypto/v2 v2.0.0
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/bds421/rho-kit/flags/v2 v2.0.0
	github.com/bds421/rho-kit/grpcx/v2 v2.0.0
	github.com/bds421/rho-kit/httpx/v2 v2.0.0
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/natsbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/redis/v2 v2.0.0
	github.com/bds421/rho-kit/infra/sqldb/pgx/v2 v2.0.0
	github.com/bds421/rho-kit/observability/v2 v2.0.0
	github.com/bds421/rho-kit/runtime/v2 v2.0.0
	github.com/bds421/rho-kit/security/v2 v2.0.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/prometheus/client_golang v1.23.2
	github.com/redis/go-redis/v9 v9.18.0
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.80.0
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/resilience/v2 v2.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.0.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc/v3 v3.0.2 // indirect
	github.com/lestrrat-go/jwx/v3 v3.0.13 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.51.0 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/open-feature/go-sdk v1.16.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/pressly/goose/v3 v3.27.0 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.19.2 // indirect
	github.com/rabbitmq/amqp091-go v1.10.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	github.com/valyala/fastjson v1.6.7 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.67.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.67.0 // indirect
	go.opentelemetry.io/otel v1.42.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.42.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.42.0 // indirect
	go.opentelemetry.io/otel/metric v1.42.0 // indirect
	go.opentelemetry.io/otel/sdk v1.42.0 // indirect
	go.opentelemetry.io/otel/trace v1.42.0 // indirect
	go.opentelemetry.io/proto/otlp v1.9.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/mock v0.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260209200024-4cfbd4190f57 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/authz/v2 => ../authz

replace github.com/bds421/rho-kit/core/v2 => ../core

replace github.com/bds421/rho-kit/crypto/v2 => ../crypto

replace github.com/bds421/rho-kit/data/v2 => ../data

replace github.com/bds421/rho-kit/flags/v2 => ../flags

replace github.com/bds421/rho-kit/grpcx/v2 => ../grpcx

replace github.com/bds421/rho-kit/httpx/v2 => ../httpx

replace github.com/bds421/rho-kit/infra/v2 => ../infra

replace github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 => ../infra/messaging/amqpbackend

replace github.com/bds421/rho-kit/infra/messaging/natsbackend/v2 => ../infra/messaging/natsbackend

replace github.com/bds421/rho-kit/infra/redis/v2 => ../infra/redis

replace github.com/bds421/rho-kit/infra/sqldb/pgx/v2 => ../infra/sqldb/pgx

replace github.com/bds421/rho-kit/io/v2 => ../io

replace github.com/bds421/rho-kit/observability/v2 => ../observability

replace github.com/bds421/rho-kit/resilience/v2 => ../resilience

replace github.com/bds421/rho-kit/runtime/v2 => ../runtime

replace github.com/bds421/rho-kit/security/v2 => ../security
