# ADR 0002: Contract Artifacts and Compatibility

## Status

Accepted.

## Context

The kit can generate OpenAPI 3.1 from typed HTTP handlers and validate
versioned JSON event schemas at runtime. Those mechanisms do not establish a
reviewable artifact, a compatibility baseline, or a CI verdict.

## Decision

Contract lifecycle v1 supports two artifact kinds only:

1. OpenAPI 3.1 JSON for HTTP APIs.
2. JSON Schema 2020-12 for JSON events.

Every artifact is described by a small JSON manifest with an immutable
identity, owner, kind, semantic version, relative document path, compatibility
policy, and optional reviewed waiver/deprecation data. Artifacts live in a
service-owned directory and are portable; the kit does not require a contract
registry service.

The initial command will validate manifests/artifacts and compare a candidate
directory with a baseline directory. It emits both human-readable and JSON
results suitable for CI. It does not generate clients, publish remote state,
or introduce protobuf support in v1.

Compatibility is conservative. Unsupported schema constructs are reported as
unknown and fail the default policy. A waiver must be explicit in the manifest
and visible in the result; a compatibility check may never silently ignore a
change.

## Consequences

- HTTP and messaging remain independent runtime modules; adapters share only
  artifact format and comparison code.
- A service owns the version/transition decision for its contracts.
- Future remote registry or gRPC/protobuf support can be added as another
  artifact adapter without changing the v1 manifest identity.
