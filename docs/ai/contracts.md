# Contract lifecycle

Packages: `cmd/kit-contract`, `httpx/openapigen`,
`infra/messaging`

Use contract artifacts to make a service's HTTP and event changes reviewable
before deployment. Contract lifecycle v1 is intentionally local: artifacts are
committed with the service and CI compares them with a checked-out baseline.
It does not require a registry service or generate clients.

## Bundle layout

```
contracts/
  contracts.json
  openapi.json
  events/order-created.schema.json
```

`contracts.json` describes every document:

```json
{
  "format": 1,
  "artifacts": [
    {
      "id": "orders.http",
      "owner": "orders",
      "kind": "openapi",
      "version": "1.2.0",
      "path": "openapi.json",
      "compatibility": {"mode": "backward"}
    },
    {
      "id": "orders.created",
      "owner": "orders",
      "kind": "event-jsonschema",
      "version": "1.2.0",
      "path": "events/order-created.schema.json",
      "schema_version": 1,
      "compatibility": {"mode": "backward"}
    }
  ]
}
```

Artifact IDs are stable fleet identifiers; paths are only local storage. Do not
reuse an event artifact ID with a new `schema_version`: publish a separately
transitioned artifact instead so consumers can migrate intentionally.

## Generate and check

`openapigen.Spec.Marshal()` returns the OpenAPI document to write as
`contracts/openapi.json`. Event documents are the exact JSON Schema bytes
registered with `messaging.SchemaRegistry`; write those same bytes into the
bundle. This keeps generated/runtime contract material identical without
making HTTP depend on a broker.

```bash
# Validate artifact shape and the supported compatibility vocabulary.
go run github.com/bds421/rho-kit/cmd/kit-contract/v2 validate -dir contracts

# Compare the current branch with a checked-out main/release baseline.
go run github.com/bds421/rho-kit/cmd/kit-contract/v2 compare \
  -candidate contracts -baseline ../baseline/contracts -format json
```

The command exits non-zero for incompatible changes. CI should retain the JSON
report as an artifact so a reviewed waiver remains visible.

## Compatibility policy

The checker is deliberately conservative:

- HTTP: removing a path/operation/status, removing documented response fields,
  changing field types, or adding required request fields is incompatible.
- Events: removing properties, changing their types, adding required input
  fields, or changing `schema_version` under the same artifact ID is
  incompatible.
- Unsupported JSON Schema keywords fail validation rather than yielding an
  optimistic answer.

A temporary waiver belongs on the candidate artifact and requires a finding
code, reason, and ISO date expiry. It does not hide the finding; the report
marks it `waived`.

```json
"waivers": [{
  "code": "http_response_removed",
  "reason": "v1 endpoint retirement approved in RFC-42",
  "expires_at": "2026-10-01"
}]
```

Do not use a waiver for an unplanned migration. Publish compatible overlap or
a new event/version and test the producer/consumer transition.
