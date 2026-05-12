# AMQP Messaging Alerts

## Alerts

- `RhoKitAMQPPublishFailuresHigh`
- `RhoKitAMQPDLQPublishFailures`
- `RhoKitAMQPForceDiscard`

## First Checks

1. Open the AMQP direct messaging dashboard and filter to the alerting
   `namespace`, `service`, `exchange`, `routing_key`, or `queue`.
2. Check `amqp_published_total{outcome!="success"}` by outcome:
   `unroutable` usually means a missing binding, `too_large` means the route
   needs `WithRouteMaxMessageBytes`, and `failed` points to channel, confirm,
   auth, or broker availability.
3. Check `amqp_consumed_total` outcomes for `retry`, `dead_lettered`,
   `dlq_publish_failed`, and `force_discarded`.
4. Inspect RabbitMQ topology for the main exchange, retry exchange, dead
   exchange, and all queue bindings produced by `amqpbackend.DeclareAll`.

## Mitigation

- For `unroutable`, restore the queue binding or stop publishing that routing
  key. The AMQP publisher uses mandatory publish and treats returned messages
  as failures by design.
- For `too_large`, add an exact route override with
  `WithRouteMaxMessageBytes(exchange, routingKey, maxBytes)` only for the
  event type that needs it.
- For `dlq_publish_failed`, fix the dead exchange and dead queue bindings
  before restarting consumers. The consumer nacks back into the retry path
  until the safety cap is reached.
- For `force_discarded`, preserve broker logs and consumer logs before
  remediation. Messages have been deliberately acked to break an infinite
  retry loop.

## Metric Contract

- `amqp_published_total{exchange,routing_key,outcome}`
- `amqp_publish_duration_seconds{exchange,routing_key,outcome}`
- `amqp_consumed_total{queue,outcome}`
- `amqp_handler_duration_seconds{queue,outcome}`

Valid publish outcomes are `success`, `failed`, `invalid_message`,
`too_large`, and `unroutable`. Valid consume outcomes are `acked`,
`ack_failed`, `decode_error`, `retry`, `dead_lettered`, `discarded`,
`force_discarded`, and `dlq_publish_failed`.
