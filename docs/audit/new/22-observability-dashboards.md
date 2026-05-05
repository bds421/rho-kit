# NEW: observability/dashboards (Grafana + Prometheus pack)

**Phase**: 3 (DX)
**Module path**: ships YAML/JSON, not Go — placed at `observability/dashboards/`

## Why

Every service emits the same RED metrics on the same buckets, the same SLO time-series, the same dependency latencies. Today every team rebuilds the dashboards and alerts. The kit should ship them.

A single `kit-new` invocation should produce a service that's already observable, with copy-paste-ready Grafana dashboards and Prometheus alerts pointing at the metrics the kit emits.

## Contents

```
observability/dashboards/
├── grafana/
│   ├── http-red.json              # HTTP RED per route + status
│   ├── grpc-red.json              # gRPC RED per service/method
│   ├── db-postgres.json           # connection pool, query duration, errors
│   ├── db-mysql.json
│   ├── redis.json                 # ops/sec, latency, pool, eviction
│   ├── messaging-amqp.json        # publish/consume rates, redeliveries, DLQ
│   ├── messaging-redis-streams.json
│   ├── storage.json               # backend latency, errors, sizes
│   ├── outbox.json                # pending/published/failed counts, age
│   ├── ratelimit.json             # allowed/denied per limiter
│   ├── runtime-go.json            # goroutines, GC, heap, MaxRSS
│   └── service-overview.json      # one screen, links to the rest
├── prometheus/
│   ├── recording-rules.yaml       # latency p50/p95/p99 from histograms
│   ├── alerts-availability.yaml   # error-rate burn for HTTP + gRPC
│   ├── alerts-latency.yaml        # latency burn
│   ├── alerts-saturation.yaml     # pool exhaustion, queue depth
│   └── slo-templates.yaml         # multi-window multi-burn-rate
└── README.md                      # how to install (file_sd / configMap / Helm)
```

## Conventions

- All dashboards parameterize on `{service, namespace}` Grafana variables.
- All alerts include `runbook_url` annotation pointing at `docs/ai/runbooks/`.
- Recording rules pre-aggregate p50/p95/p99 from `histogram_quantile` to keep ad-hoc queries cheap.
- SLO templates use the multi-window multi-burn-rate pattern (1h@14.4×, 6h@6×).

## Coupling to metric names

The kit's `observability/redmetrics` (proposed in [new/16](16-observability-red-metrics.md)) emits a stable set of metric names with stable label sets. Dashboards depend on those names; `kit-doctor` warns if a service overrides them in incompatible ways.

## Distribution

- Files live in the repo so `kit-new` can stamp them into a generated service's `deploy/` folder.
- A `helm/rho-kit-observability/` chart published from the same files lets larger orgs install centrally.
- A small wrapper command `kit observability install --grafana-url=... --token=...` uploads dashboards via Grafana API for orgs without GitOps.

## Definition of done

- [ ] Dashboard JSON for HTTP, gRPC, DB, Redis, messaging, storage, outbox, runtime, ratelimit.
- [ ] Recording rules + alert templates per category.
- [ ] SLO multi-burn-rate templates.
- [ ] README with install methods (Helm, file_sd, Grafana API).
- [ ] `kit-new` stamps `deploy/grafana/` and `deploy/prometheus/`.
- [ ] CI test that exports dashboards as code (`grafonnet` or similar) so they stay versionable.

## Related

- [new/16-observability-red-metrics.md](16-observability-red-metrics.md) — the metric names these dashboards consume.
- [new/15-observability-pprof-runtime.md](15-observability-pprof-runtime.md) — `runtime-go.json` uses these.
- [new/21-tools-kit-new.md](21-tools-kit-new.md) — generator stamps the deploy/ tree.
