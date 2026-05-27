module github.com/bds421/rho-kit/testing/integrationtest/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/crypto/v2 v2.0.0
	github.com/bds421/rho-kit/data/actionlog/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/data/approval/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/data/budget/redis/v2 v2.0.0
	github.com/bds421/rho-kit/data/cache/rediscache/v2 v2.0.0
	github.com/bds421/rho-kit/data/cron/pgstore/v2 v2.0.0
	github.com/bds421/rho-kit/data/idempotency/pgstore/v2 v2.0.0
	github.com/bds421/rho-kit/data/saga/pgstore/v2 v2.0.0
	github.com/bds421/rho-kit/data/idempotency/redisstore/v2 v2.0.0
	github.com/bds421/rho-kit/data/lock/pgadvisory/v2 v2.0.0
	github.com/bds421/rho-kit/data/lock/redislock/v2 v2.0.0
	github.com/bds421/rho-kit/data/queue/redisqueue/v2 v2.0.0
	github.com/bds421/rho-kit/data/queue/riverqueue/v2 v2.0.0
	github.com/bds421/rho-kit/data/ratelimit/redis/v2 v2.0.0
	github.com/bds421/rho-kit/data/stream/redisstream/v2 v2.0.0
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/bds421/rho-kit/httpx/websocket/v2 v2.0.0
	github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2 v2.0.0
	github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2 v2.0.0
	github.com/bds421/rho-kit/infra/leaderelection/redislock/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/natsbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/messaging/redisbackend/v2 v2.0.0
	github.com/bds421/rho-kit/infra/outbox/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/infra/redis/redistest/v2 v2.0.0
	github.com/bds421/rho-kit/infra/redis/v2 v2.0.0
	github.com/bds421/rho-kit/infra/sqldb/dbtest/v2 v2.0.0
	github.com/bds421/rho-kit/infra/sqldb/pgx/v2 v2.0.0
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/observability/auditlog/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/observability/v2 v2.0.0
	github.com/bds421/rho-kit/testing/kittest/v2 v2.0.0
	github.com/coder/websocket v1.8.14
	github.com/google/uuid v1.6.0
	github.com/hibiken/asynq v0.25.1
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nats-io/nats.go v1.52.0
	github.com/pressly/goose/v3 v3.27.1
	github.com/rabbitmq/amqp091-go v1.11.0
	github.com/redis/go-redis/v9 v9.19.0
	github.com/riverqueue/river v0.37.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.37.0
	github.com/segmentio/kafka-go v0.4.51
	github.com/stretchr/testify v1.11.1
	github.com/testcontainers/testcontainers-go v0.42.0
	github.com/testcontainers/testcontainers-go/modules/kafka v0.42.0
	github.com/testcontainers/testcontainers-go/modules/nats v0.42.0
	github.com/testcontainers/testcontainers-go/modules/rabbitmq v0.42.0
	k8s.io/apimachinery v0.36.1
	k8s.io/client-go v0.36.1
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20250102033503-faa5f7b0171c // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/bds421/rho-kit/resilience/v2 v2.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/platforms v0.2.1 // indirect
	github.com/cpuguy83/dockercfg v0.3.2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-redsync/redsync/v4 v4.16.0 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/magiconair/properties v1.8.10 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mdelapenya/tlscert v0.2.0 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/go-archive v0.2.0 // indirect
	github.com/moby/moby/api v1.54.2 // indirect
	github.com/moby/moby/client v0.4.1 // indirect
	github.com/moby/patternmatcher v0.6.1 // indirect
	github.com/moby/sys/sequential v0.6.0 // indirect
	github.com/moby/sys/user v0.4.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/moby/term v0.5.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/riverqueue/river/riverdriver v0.37.0 // indirect
	github.com/riverqueue/river/rivershared v0.37.0 // indirect
	github.com/riverqueue/river/rivertype v0.37.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/shirou/gopsutil/v4 v4.26.3 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/spf13/cast v1.7.0 // indirect
	github.com/testcontainers/testcontainers-go/modules/postgres v0.42.0 // indirect
	github.com/testcontainers/testcontainers-go/modules/redis v0.42.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tink-crypto/tink-go/v2 v2.6.0 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.68.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/goleak v1.3.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/mod v0.36.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/term v0.43.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.36.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260317180543-43fb72c5454a // indirect
	k8s.io/utils v0.0.0-20260210185600-b8788abfbbc2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.3.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../core

replace github.com/bds421/rho-kit/crypto/v2 => ../../crypto

replace github.com/bds421/rho-kit/data/v2 => ../../data

replace github.com/bds421/rho-kit/data/actionlog/postgres/v2 => ../../data/actionlog/postgres

replace github.com/bds421/rho-kit/data/approval/postgres/v2 => ../../data/approval/postgres

replace github.com/bds421/rho-kit/data/budget/redis/v2 => ../../data/budget/redis

replace github.com/bds421/rho-kit/data/cache/rediscache/v2 => ../../data/cache/rediscache

replace github.com/bds421/rho-kit/data/cron/pgstore/v2 => ../../data/cron/pgstore

replace github.com/bds421/rho-kit/data/idempotency/pgstore/v2 => ../../data/idempotency/pgstore

replace github.com/bds421/rho-kit/data/saga/pgstore/v2 => ../../data/saga/pgstore

replace github.com/bds421/rho-kit/data/idempotency/redisstore/v2 => ../../data/idempotency/redisstore

replace github.com/bds421/rho-kit/data/lock/pgadvisory/v2 => ../../data/lock/pgadvisory

replace github.com/bds421/rho-kit/data/lock/redislock/v2 => ../../data/lock/redislock

replace github.com/bds421/rho-kit/data/queue/redisqueue/v2 => ../../data/queue/redisqueue

replace github.com/bds421/rho-kit/data/queue/riverqueue/v2 => ../../data/queue/riverqueue

replace github.com/bds421/rho-kit/data/ratelimit/redis/v2 => ../../data/ratelimit/redis

replace github.com/bds421/rho-kit/data/stream/redisstream/v2 => ../../data/stream/redisstream

replace github.com/bds421/rho-kit/httpx/websocket/v2 => ../../httpx/websocket

replace github.com/bds421/rho-kit/infra/v2 => ../../infra

replace github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2 => ../../infra/leaderelection/k8slease

replace github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2 => ../../infra/leaderelection/pgadvisory

replace github.com/bds421/rho-kit/infra/leaderelection/redislock/v2 => ../../infra/leaderelection/redislock

replace github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2 => ../../infra/messaging/amqpbackend

replace github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2 => ../../infra/messaging/kafkabackend

replace github.com/bds421/rho-kit/infra/messaging/natsbackend/v2 => ../../infra/messaging/natsbackend

replace github.com/bds421/rho-kit/infra/messaging/redisbackend/v2 => ../../infra/messaging/redisbackend

replace github.com/bds421/rho-kit/infra/outbox/postgres/v2 => ../../infra/outbox/postgres

replace github.com/bds421/rho-kit/infra/redis/v2 => ../../infra/redis

replace github.com/bds421/rho-kit/infra/redis/redistest/v2 => ../../infra/redis/redistest

replace github.com/bds421/rho-kit/infra/sqldb/dbtest/v2 => ../../infra/sqldb/dbtest

replace github.com/bds421/rho-kit/infra/sqldb/pgx/v2 => ../../infra/sqldb/pgx

replace github.com/bds421/rho-kit/io/v2 => ../../io

replace github.com/bds421/rho-kit/observability/v2 => ../../observability

replace github.com/bds421/rho-kit/observability/auditlog/postgres/v2 => ../../observability/auditlog/postgres

replace github.com/bds421/rho-kit/resilience/v2 => ../../resilience

replace github.com/bds421/rho-kit/runtime/v2 => ../../runtime

replace github.com/bds421/rho-kit/infra/storage/s3backend/v2 => ../../infra/storage/s3backend

replace github.com/bds421/rho-kit/infra/storage/sftpbackend/v2 => ../../infra/storage/sftpbackend

replace github.com/bds421/rho-kit/infra/storage/storagetest/v2 => ../../infra/storage/storagetest

replace github.com/bds421/rho-kit/security/v2 => ../../security

replace github.com/bds421/rho-kit/testing/kittest/v2 => ../kittest
