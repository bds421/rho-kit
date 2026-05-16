# observability/dashboards

Grafana dashboards and Prometheus rules / alert templates that consume
the metric names emitted by the rho-kit's various packages
(`observability/redmetrics`, `observability/runtimemetrics`,
`grpcx`, `infra/sqldb`, `infra/redis`, `infra/storage`,
`infra/outbox`, `infra/messaging/amqpbackend`,
`infra/messaging/natsbackend`, `data/stream/redisstream`,
`httpx/middleware/ratelimit`).

The dashboards parameterize on `{service, namespace}` Grafana
variables so a single JSON file serves every consuming service. The
Prometheus rules group recording rules and alerts by category so an
operator can opt in incrementally.

## Layout

```
grafana/
  http-red.json          # HTTP: request rate, error rate, p50/p95/p99
  grpc-red.json          # gRPC: request rate, error rate, status codes,
                         #   p50/p95/p99 (grpc_server_handled_total /
                         #   grpc_server_handling_seconds)
  runtime-go.json        # goroutines, threads, heap, GC, max RSS
  service-overview.json  # one screen with links to the per-area dashboards
  db-pool.json           # PostgreSQL pool (pgx-backed): open / idle /
                         #   in-use / wait rate (matches __name__ regex
                         #   on <service>_db_open_connections etc.)
  redis.json             # Redis: command rate / errors / p50/p95/p99,
                         #   pool stats, reconnects, healthy gauge
  storage.json           # Storage provider overview: S3/GCS/Azure/SFTP
  storage-s3.json        # S3: rate / errors / ratios / p50/p95/p99
  storage-gcs.json       # GCS: rate / errors / ratios / p50/p95/p99
  storage-azure.json     # Azure Blob: rate / errors / ratios / p50/p95/p99
  storage-sftp.json      # SFTP: rate / errors / p50/p95/p99 plus
                         #   connection-healthy gauge
  outbox.json            # Outbox relay: pending depth, publish rate,
                         #   error ratio, p50/p95/p99 relay latency
  amqp.json              # Direct AMQP: publish/consume outcomes,
                         #   latency, retry/DLQ/discard rates
  nats.json              # Direct NATS JetStream: publish/consume outcomes,
                         #   latency, retry/termination/finalization rates
  redis-stream.json      # Direct Redis Streams: produced/consumed rates,
                         #   failure/dead-letter rates, pending depth,
                         #   p50/p95/p99 processing latency
  ratelimit.json         # HTTP rate limits: decisions, limited ratio,
                         #   Retry-After distribution, degradation
  leaderelection.json    # OnAcquired callback-drain duration,
                         #   warn-tick rate, timeout terminal events
                         #   (covers k8slease/etcd/pgadvisory/redislock)
  centrifuge.json        # Realtime centrifuge: connect outcomes,
                         #   reject ratio, disconnects, subscribes /
                         #   publishes by channel class
  grpc-stream-limits.json # Server-wide active streams, ResourceExhausted
                         #   rejections, kit idle-timeout closures

prometheus/
  recording-rules.yaml      # pre-aggregated p50/p95/p99 for HTTP, gRPC,
                            #   Redis, storage, outbox, AMQP, NATS, and
                            #   Redis Stream histograms plus rate-limit ratios
  alerts-availability.yaml  # 5xx ratio, 4xx ratio
  alerts-latency.yaml       # HTTP p99 thresholds
  alerts-saturation.yaml    # DB pool waits/near-exhaustion, outbox
                            #   backlog growth, Redis pool timeouts
  alerts-messaging.yaml     # outbox error rate, no-progress, relay p99,
                            #   AMQP, NATS, and Redis Stream alerts
  alerts-ratelimit.yaml     # rate-limit spikes, degradation, unavailable
  alerts-coordination.yaml  # leader-election drain, centrifuge auth /
                            #   connect-error rates, gRPC stream cap
                            #   rejecting + idle-close spikes
  slo-templates.yaml        # multi-window multi-burn-rate SLO rules
```

## Coupling to metric names

Dashboards depend on the stable metric names emitted by:

- `observability/redmetrics`: `http_requests_total`,
  `http_errors_total`, `http_request_duration_seconds`,
  `http_requests_in_flight`.
- `observability/runtimemetrics`: `go_goroutines`, `go_threads`,
  `go_heap_alloc_bytes`, `go_heap_sys_bytes`, `go_gc_pause_seconds_sum`,
  `go_gc_count_total`, `go_max_rss_bytes`.
- `grpcx/interceptor`: `grpc_server_handled_total`,
  `grpc_server_handling_seconds`.
- `infra/sqldb`: `<service>_db_open_connections`,
  `<service>_db_idle_connections`,
  `<service>_db_wait_count_total` — service-prefixed because
  `NewPoolMetrics` takes the service namespace as a metric prefix.
  Dashboards match these via `{__name__=~".+_db_open_connections"}`.
- `infra/redis`: `redis_command_duration_seconds`,
  `redis_command_errors_total`, `redis_pool_*`,
  `redis_reconnect_*`, `redis_connection_healthy`.
- `infra/storage/{s3backend,gcsbackend,azurebackend,sftpbackend}`:
  `storage_s3_operation_duration_seconds`,
  `storage_gcs_operation_duration_seconds`,
  `storage_azure_operation_duration_seconds`,
  `storage_sftp_operation_duration_seconds`, plus matching
  `*_errors_total` and the SFTP `connection_healthy` gauge.
- `infra/outbox`: `outbox_pending_count`,
  `outbox_relay_latency_seconds`, `outbox_published_total`,
  `outbox_errors_total`.
- `infra/messaging/amqpbackend`: `amqp_published_total`,
  `amqp_publish_duration_seconds`, `amqp_consumed_total`,
  `amqp_handler_duration_seconds`. Labels are limited to
  `exchange`, `routing_key`, `queue`, and `outcome`.
- `infra/messaging/natsbackend`: `nats_published_total`,
  `nats_publish_duration_seconds`, `nats_consumed_total`,
  `nats_handler_duration_seconds`. Labels are limited to
  `exchange`, `routing_key`, `stream`, `durable`, and `outcome`.
- `data/stream/redisstream` and `infra/messaging/redisbackend`:
  `redis_stream_messages_produced_total`,
  `redis_stream_messages_consumed_total`,
  `redis_stream_messages_failed_total`,
  `redis_stream_messages_dead_lettered_total`,
  `redis_stream_processing_duration_seconds`,
  `redis_stream_pending_messages`. Labels are `stream` and `group`,
  rendered as opaque stable values instead of raw Redis names.
- `httpx/middleware/ratelimit`: `http_ratelimit_decisions_total`
  and `http_ratelimit_retry_after_seconds`. Labels are `limiter`,
  `kind`, and `outcome`; raw keys, IPs, tenants, users, and paths are
  not labels.

If a service overrides `WithHTTPNamespace` / `WithHTTPSubsystem`,
dashboards must be templated against the new namespace at install
time. `kit-doctor` will warn on incompatible overrides.

## Install methods

### File-based (recommended for GitOps)

Sync the JSON / YAML directories into your Grafana provisioning and
Prometheus rule directories:

```yaml
# Grafana provisioning
apiVersion: 1
providers:
  - name: rho-kit
    folder: rho-kit
    type: file
    options:
      path: /var/lib/grafana/dashboards/rho-kit
```

```yaml
# Prometheus
rule_files:
  - /etc/prometheus/rules/rho-kit/*.yaml
```

### Kit-new generated services

`kit-new` stamps `deploy/grafana/` and `deploy/prometheus/` into the
generated tree pointing at this same content. The generated service's
CI runs `promtool check rules` on the alert files.

### Helm

A `helm/rho-kit-observability/` chart (TODO) wraps these files for
orgs that want centralized rollout.

## What to do when an alert fires

Each alert's `runbook_url` annotation links to a runbook under
`docs/ai/runbooks/`. The runbooks ship in the same repo so they
travel with the alerts:

- `availability.md` — `RhoKitHighErrorRate`, `RhoKitElevated4xxRate`
- `latency.md` — `RhoKitLatencyP99High`, `RhoKitLatencyP99Critical`
- `slo.md` — `RhoKitSLOFastBurn`, `RhoKitSLOSlowBurn`
- `db-saturation.md` — `RhoKitDBPoolWaiting`,
  `RhoKitDBPoolNearExhaustion`
- `outbox-backlog.md` — `RhoKitOutboxBacklogGrowing`,
  `RhoKitOutboxBacklogCritical`
- `outbox-errors.md` — `RhoKitOutboxErrorRateHigh`,
  `RhoKitOutboxNoProgress`, `RhoKitOutboxRelayLatencyHigh`
- `redis-pool.md` — `RhoKitRedisPoolTimeouts`
- `amqp-messaging.md` — `RhoKitAMQPPublishFailuresHigh`,
  `RhoKitAMQPDLQPublishFailures`, `RhoKitAMQPForceDiscard`
- `nats-messaging.md` — `RhoKitNATSPublishFailuresHigh`,
  `RhoKitNATSDeliveryFinalizationFailures`, `RhoKitNATSHandlerPanics`
- `redis-stream.md` — `RhoKitRedisStreamFailureRateHigh`,
  `RhoKitRedisStreamDeadLetters`, `RhoKitRedisStreamPendingHigh`
- `ratelimit.md` — `RhoKitRateLimitSpike`,
  `RhoKitRateLimitDegraded`, `RhoKitRateLimitUnavailable`
- `leader-election.md` — `RhoKitLeaderCallbackDrainStuck`,
  `RhoKitLeaderCallbackDrainTimeout`
- `centrifuge.md` — `RhoKitCentrifugeConnectRejectHigh`,
  `RhoKitCentrifugeConnectErrorRateHigh`
- `grpc-stream-limits.md` — `RhoKitGRPCStreamCapacityRejecting`,
  `RhoKitGRPCStreamIdleClosesSpike`
- `tracing.md` — OTel tracing reference for kit-emitted spans
  (waves 167–169); not alert-driven, used when investigating

## Local validation

The `.github/workflows/dashboards.yml` workflow runs `promtool check
rules` and `python3 -m json.tool` on every PR that touches
`observability/dashboards/**`. To run the same checks locally
before pushing:

```bash
# JSON validation (pure stdlib, no extra tools required)
for f in observability/dashboards/grafana/*.json; do
  python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$f"
done

# Prometheus rule validation (requires promtool, ships in the
# Prometheus tarball — install with: brew install prometheus, or
# download from https://prometheus.io/download/)
promtool check rules observability/dashboards/prometheus/*.yaml
```

If `promtool` isn't installed locally, the CI workflow is the
canonical validator and will run on PRs.

## Definition of done

This pack covers HTTP RED, gRPC RED, Go runtime, service overview,
DB pool, Redis, Storage (S3/GCS/Azure/SFTP overview and provider
dashboards), Outbox, direct AMQP messaging, direct NATS JetStream messaging,
direct Redis Streams messaging, HTTP rate-limit, leader election (all
adapters), realtime centrifuge, and gRPC stream-limit dashboards
plus the matching alerts
(availability, latency, saturation, messaging, rate-limit,
coordination, SLO multi-burn-rate).
