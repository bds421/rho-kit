# ADR 0004: Production Reference Baseline

## Status

Accepted.

## Context

Existing examples deliberately use in-memory components to teach isolated
concepts. A product team needs one reference that proves the combined
production boundary without imposing a deployment platform on all users.

## Decision

The reference service will be deployment-neutral and Docker-testable. Its
required dependencies are Postgres, Redis, and one supported broker selected
for the reference (NATS JetStream unless integration evidence shows a stronger
reason to choose another existing adapter). It uses:

- resource JWT authentication and canonical principal projection;
- an authorization-decider seam;
- Postgres migrations, transactional inbox, and Postgres outbox;
- health, metrics, tracing, bounded graceful shutdown, and secret/key rotation
  proof; and
- contract artifact generation and compatibility checking.

The reference is not a Helm chart, service mesh, identity provider, or generic
deployment framework. It may include Docker Compose/test configuration solely
to prove dependency lifecycle and integration behaviour.

## Consequences

- `kit-new` can later scaffold a visible subset of this composition.
- The release gate gains end-to-end evidence against real dependencies without
  making ordinary kit package tests depend on Docker.
