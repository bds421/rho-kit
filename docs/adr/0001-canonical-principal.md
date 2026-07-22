# ADR 0001: Canonical Principal

## Status

Accepted.

## Context

The kit currently authenticates JWTs, API keys, mTLS callers, PASETO tokens,
and browser OIDC sessions through separate paths. `authz.Decider`, audit, HTTP,
and gRPC need the same stable identity without receiving provider-specific
claim shapes.

## Decision

Add a transport-neutral `security/identity.Principal` in the existing identity
package. Its stable fields are:

| Field | Meaning | Logging/audit rule |
|---|---|---|
| `Subject` | Immutable principal subject; normally the issuer's `sub` | Treat as an identifier; redact from ordinary logs |
| `Actor` | Effective caller used for audit; may differ during delegation | Treat as an identifier; redact from ordinary logs |
| `Kind` | `user`, `api_key`, `oauth_client`, or `service` | Safe bounded enum |
| `Tenant` | Stable tenant/org identifier if authenticated | Treat as an identifier; redact from ordinary logs |
| `Scopes` | Granted OAuth/API scopes | Bounded policy input; never trust unverified transport headers |
| `Permissions` | Provider/projected permissions | Bounded policy input; never trust unverified transport headers |
| `Claims` | Explicit allow-listed, normalized provider claims | Never retain raw provider token or arbitrary claims by default |

`Claims` is not an opaque dump of JWT/OIDC claims. A declarative mapping profile
selects and validates allowed fields. The default profile maps only a verified
subject and therefore cannot accidentally make an unreviewed provider claim a
business authorization input.

HTTP and gRPC will expose adapters to/from their existing identity context
types during migration. `authz.Decider` continues to receive the existing
subject/action/resource triple; applications choose the resource and action,
while the canonical principal supplies the subject and audit actor.

## Consequences

- Provider-specific modules are unnecessary: Auth0, Ory, Cognito, Keycloak,
  and Firebase fit via mapping profiles and fixtures.
- Services have one identity object for audit and authorization.
- User management, password recovery, credential ceremony, and organisation
  administration remain outside the kit.
