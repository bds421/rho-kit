# observability/dashboards

Grafana dashboards and Prometheus rules / alert templates that consume
the metric names emitted by the rho-kit's `observability/redmetrics`
and `observability/runtimemetrics` packages.

The dashboards parameterize on `{service, namespace}` Grafana
variables so a single JSON file serves every consuming service. The
Prometheus rules group recording rules and alerts by category so an
operator can opt in incrementally.

## Layout

```
grafana/
  http-red.json          # request rate, error rate, p50/p95/p99 latency per route
  runtime-go.json        # goroutines, threads, heap, GC, max RSS
  service-overview.json  # one screen with links to the per-area dashboards

prometheus/
  recording-rules.yaml   # pre-aggregated p50/p95/p99 from histograms
  alerts-availability.yaml
  alerts-latency.yaml
  slo-templates.yaml     # multi-window multi-burn-rate SLO rules
```

## Coupling to metric names

Dashboards depend on the stable metric names emitted by:

- `observability/redmetrics`: `http_requests_total`,
  `http_errors_total`, `http_request_duration_seconds`,
  `http_requests_in_flight`.
- `observability/runtimemetrics`: `go_goroutines`, `go_threads`,
  `go_heap_alloc_bytes`, `go_heap_sys_bytes`, `go_gc_pause_seconds_sum`,
  `go_gc_count_total`, `go_max_rss_bytes`.

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

Each alert's `runbook_url` annotation links to a runbook. Until the
runbooks ship:

- **availability burn**: error rate exceeds the SLO budget burn
  threshold over the configured window. Roll back the latest deploy
  if the burn started < deploy ago, otherwise page the on-call.
- **latency burn**: p99 has exceeded the SLO. Check upstream
  dependencies first (DB pool, external HTTP) before assuming a code
  regression.

## Definition of done

This pack covers the highest-impact dashboards (HTTP RED, Go runtime,
service overview) and the core SLO multi-burn-rate templates. The
remaining dashboards listed in
`docs/audit/new/22-observability-dashboards.md` (gRPC, DB, Redis,
messaging, storage, outbox, ratelimit) are deferred and will land as
each kit area's metric surface stabilises.
