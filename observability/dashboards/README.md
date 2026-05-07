# observability/dashboards

Grafana dashboards and Prometheus rules / alert templates that consume
the metric names emitted by the rho-kit's various packages
(`observability/redmetrics`, `observability/runtimemetrics`,
`grpcx`, `infra/sqldb`, `infra/redis`, `infra/storage`,
`infra/outbox`).

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
  db-pool.json           # Postgres/MySQL pool: open / idle / in-use /
                         #   wait rate (matches __name__ regex on
                         #   <service>_db_open_connections etc.)
  redis.json             # Redis: command rate / errors / p50/p95/p99,
                         #   pool stats, reconnects, healthy gauge
  storage.json           # S3 + SFTP: rate / errors / p50/p95/p99 per
                         #   operation, sftp connection-healthy gauge
  outbox.json            # Outbox relay: pending depth, publish rate,
                         #   error ratio, p50/p95/p99 relay latency

prometheus/
  recording-rules.yaml      # pre-aggregated p50/p95/p99 for HTTP, gRPC,
                            #   Redis, storage, outbox histograms
  alerts-availability.yaml  # 5xx ratio, 4xx ratio
  alerts-latency.yaml       # HTTP p99 thresholds
  alerts-saturation.yaml    # DB pool waits/near-exhaustion, outbox
                            #   backlog growth, Redis pool timeouts
  alerts-messaging.yaml     # outbox error rate, no-progress, relay p99
  slo-templates.yaml        # multi-window multi-burn-rate SLO rules
```

### Skipped dashboards

Some kit areas don't yet emit Prometheus metrics, so a dashboard
would be empty. They're tracked here for when the metrics arrive:

- **AMQP messaging** (`infra/messaging/amqpbackend`,
  `infra/messaging/buffered_publisher.go`) — currently uses a
  callback hook (`BufferedPublisherMetrics`) rather than Prometheus
  collectors. Add an `amqp.json` dashboard once those callbacks are
  wired through to a Prometheus collector.
- **Ratelimit** (`httpx/middleware/ratelimit`) — no Prometheus
  metrics emitted today. Add a `ratelimit.json` dashboard once
  hit/throttle/degradation counters are added.

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
- `infra/storage/{s3backend,sftpbackend}`:
  `storage_s3_operation_duration_seconds`,
  `storage_sftp_operation_duration_seconds`, plus matching
  `*_errors_total` and the SFTP `connection_healthy` gauge.
- `infra/outbox`: `outbox_pending_count`,
  `outbox_relay_latency_seconds`, `outbox_published_total`,
  `outbox_errors_total`.

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
DB pool, Redis, Storage (S3 + SFTP), and Outbox dashboards plus the
matching alerts (availability, latency, saturation, messaging, SLO
multi-burn-rate). AMQP-direct messaging and ratelimit dashboards
remain deferred until those areas expose Prometheus collectors —
see "Skipped dashboards" above.
