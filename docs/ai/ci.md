# Incremental CI baseline

A downstream service can adopt the platform checks without changing its
runtime architecture. Keep the commands as separate steps so a failure names
the boundary that needs attention.

```bash
# Unit/package tests for the service.
go test ./...

# Security and composition findings; retain doctor.json as a CI artifact.
go run github.com/bds421/rho-kit/cmd/kit-doctor/v2 -format=json . > doctor.json

# Contract compatibility against a checked-out main/release baseline.
go run github.com/bds421/rho-kit/cmd/kit-contract/v2 validate -dir contracts
go run github.com/bds421/rho-kit/cmd/kit-contract/v2 compare \
  -candidate contracts -baseline ../baseline/contracts -format json

# Verify kit-managed SQL migrations have not been locally edited.
go run github.com/bds421/rho-kit/cmd/kit-migrate/v2 check --to=./migrations

# Report release-baseline gaps at the fleet or portfolio level.
go run github.com/bds421/rho-kit/cmd/kit-catalog/v2 \
  -fleet . -report -required-version v2.5.0 -format json
```

## Suppressions

`kit-doctor`'s JSON report contains an inventory of every inline allowance.
New suppressions should be attributable and time-bounded:

```go
// kit-doctor:allow rate-limit-omission owner="platform" reason="gateway enforces limit" review="2026-12-01" posture="security"
```

Use `posture="security"` when the exception changes a security control, or
`posture="unchanged"` when an equivalent reviewed control exists elsewhere.
Legacy suppressions remain effective for compatibility but appear with
`complete: false` in the inventory until metadata is added.

The default rules also reject `auth/oauth2.NewMemorySessionStore` and
`NewMemoryStateStore` outside tests. Browser OIDC deployments must use the
Redis stores so session and callback-replay state survive a replica change.
When a repository contains `contracts/`, doctor also requires its manifest;
the production profile additionally checks that its embedded event schema is
byte-identical to the published contract artifact.
