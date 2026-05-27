# Changes

## Unreleased — v2.0

- Initial release.
- RoutingPool routes default Acquire to primary, WithReadOnly Acquire
  round-robin to a healthy replica, fallback to primary with
  warn-level log when all replicas are unhealthy.
- Background health-probe loop (default 30s) marks replicas unhealthy
  after 3 consecutive failures and re-adds on recovery.
- Metrics: primary_acquires_total, replica_acquires_total,
  replica_fallback_total, replicas_healthy, replicas_total.
- Tests use an in-package fake Acquirer — no testcontainer required
  for the routing logic.
