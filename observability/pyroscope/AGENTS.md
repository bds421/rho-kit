# observability/pyroscope

## Purpose

Continuous-profiling adapter for Grafana Pyroscope / Phlare. Wraps
pyroscope-go as a `lifecycle.Component` so it starts on service start
and stops cleanly on SIGTERM.

## Public API

- `Config{ServerAddress, AppName, Tags, UploadRate, ProfileTypes, AuthToken, TenantID}`
- `Component(cfg, opts...) (*Profiler, error)`
- `Profiler.Start(ctx)` / `Profiler.Stop(ctx)`
- `WithLogger(*slog.Logger)`
- `DefaultProfileTypes()` — CPU + alloc objects + alloc space + inuse objects + inuse space

## Operational cost

Pyroscope-go takes a CPU sample every 10ms and uploads on a 15s tick
by default. Production cost: 0.5-1.5% CPU.

Tag cardinality matters: tag by `env`, `version`, `region`. **Never**
tag by user-ID, request-ID, or trace-ID — that explodes storage.

## Tests

`go test ./...`. Covers: missing-server-address rejected, missing-AppName
rejected, nil-option rejected, defaults applied, lifecycle Start/Stop
against a stub HTTP server, Stop is idempotent, double-Start rejected.

## See also

- `observability/pprof` — on-demand pprof endpoint (no upload). Cheaper
  than continuous profiling for ad-hoc analysis.
- `observability/tracing` — OTel traces (different signal, complementary).
- `observability/redmetrics` — RED metrics (third signal).
