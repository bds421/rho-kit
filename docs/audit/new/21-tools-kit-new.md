# NEW: cmd/kit-new (scaffold generator)

**Phase**: 6 (Agent-readiness; companion to `cmd/kit-doctor`)
**Module path**: `github.com/bds421/rho-kit/cmd/kit-new`

## Why

`kit-doctor` enforces correctness in existing services. `kit-new` *generates* a correct service from scratch. Together they bracket the lifecycle:

- `kit-new my-service` → service skeleton wired to the golden path with all secure defaults.
- `kit-doctor ./...` → ongoing check that the service hasn't drifted.

Agents benefit most: instead of reasoning about which `With*` calls to wire, they invoke `kit-new` and edit business logic.

## What it generates

```
my-service/
├── cmd/my-service/main.go           # golden-path bootstrap, calls app.WithProductionDefaults
├── internal/app/config.go           # typed env config with validation
├── internal/app/wire.go             # builder wiring with required options
├── internal/handlers/               # one example HTTP handler with typed JSON
├── internal/repo/                   # one example GORM model + repository
├── migrations/                      # one example migration (tenant_id column, audit table)
├── deploy/k8s/                      # Deployment, Service, ConfigMap, Secret, ServiceMonitor
├── deploy/grafana/                  # RED dashboard JSON (from new/22)
├── deploy/prometheus/               # SLO + burn-rate alerts (from new/22)
├── test/integration/                # one example test using dbtest+redistest
├── test/e2e/                        # one example smoke test
├── AGENTS.md                        # service-local agent guide
├── README.md                        # human guide
├── Makefile                         # targets matching kit conventions
├── go.mod                           # depends on rho-kit + chosen extras
└── .github/workflows/ci.yml         # build, test, lint, vulncheck, kit-doctor
```

## Flags

```
kit-new <service-name>
  --dir=<output-path>              (default: ./<service-name>)
  --modules=db,redis,amqp,jwt      (which infra to wire — default: db,jwt,metrics)
  --tenant                         (include multi-tenant primitives from new/20)
  --token=jwt|paseto               (which token format — default: jwt for compat)
  --license=...
  --module-path=github.com/.../my-service
```

## Templates

Templates live in `cmd/kit-new/templates/` as Go `text/template` files. Each `--modules` flag toggles inclusion of conditional blocks. Snapshot tests verify generated output compiles and passes `kit-doctor`.

## Self-test

`kit-new --self-test` generates a service into a temp dir, runs `go build`, `go test`, and `kit-doctor` against it. CI runs this on every kit release so generated services never bit-rot.

## Definition of done

- [ ] CLI binary with the flags above.
- [ ] Templates for the file tree above.
- [ ] Generated service compiles and passes `kit-doctor`.
- [ ] Snapshot tests for representative module combinations.
- [ ] `--self-test` mode in CI.
- [ ] Doc explaining how to add a new template.

## Related

- [new/18-tools-kit-doctor.md](18-tools-kit-doctor.md) — sibling tool.
- [new/19-app-production-defaults.md](19-app-production-defaults.md) — generated `wire.go` calls this.
- [new/22-observability-dashboards.md](22-observability-dashboards.md) — generated `deploy/grafana/` content.
