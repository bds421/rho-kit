# Redis Stream Messaging Alerts

## Alerts

- `RhoKitRedisStreamFailureRateHigh`
- `RhoKitRedisStreamDeadLetters`
- `RhoKitRedisStreamPendingHigh`

## First Checks

1. Open the Redis Streams direct messaging dashboard and filter to the alerting
   `namespace`, `service`, `stream`, and `group`.
2. Compare `redis_stream_messages_consumed_total`,
   `redis_stream_messages_failed_total`, and
   `redis_stream_messages_dead_lettered_total` over the same window.
3. Check `redis_stream_pending_messages` to separate a slow consumer from a
   hard handler failure.
4. Inspect the service logs for handler errors and the dead-letter stream for
   repeated message types or payload shapes.

## Mitigation

- For high failure ratio, fix the handler error first. Increasing concurrency
  only raises Redis pressure when each delivery fails.
- For dead letters, inspect and preserve the dead-letter entries before
  replaying. Replaying without fixing the handler usually creates another
  dead-letter cycle.
- For high pending entries with low failure rate, add consumers, reduce handler
  latency, or split high-volume streams by stable route.
- If Redis command latency or pool timeouts rise at the same time, follow the
  Redis pool runbook before changing stream consumer settings.

## Metric Contract

- `redis_stream_messages_produced_total{stream}`
- `redis_stream_messages_consumed_total{stream,group}`
- `redis_stream_messages_failed_total{stream,group}`
- `redis_stream_messages_dead_lettered_total{stream,group}`
- `redis_stream_processing_duration_seconds{stream,group}`
- `redis_stream_pending_messages{stream,group}`

`stream` and `group` label values are opaque stable labels derived with
`promutil.OpaqueLabelValue`; they are suitable for equality, grouping, and
dashboards but intentionally do not expose raw Redis stream or group names.
