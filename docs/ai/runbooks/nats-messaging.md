# NATS Messaging Alerts

## Alerts

- `RhoKitNATSPublishFailuresHigh`
- `RhoKitNATSDeliveryFinalizationFailures`
- `RhoKitNATSHandlerPanics`

## First Checks

1. Open the NATS JetStream direct messaging dashboard and filter to the
   alerting `namespace`, `service`, `exchange`, `routing_key`, `stream`, or
   `durable`.
2. Check `nats_published_total{outcome!="success"}` by outcome:
   `too_large` means the route needs `WithRouteMaxMessageBytes`,
   `invalid_message` points to message construction, and `failed` points to
   broker, stream, auth, or timeout issues.
3. Check `nats_consumed_total` outcomes for `retry`, `ack_failed`,
   `nak_failed`, `term_failed`, `permanent`, `decode_error`, and
   `handler_panic`.
4. Inspect the JetStream stream subjects, durable consumer state, pending
   count, and recent `MaxDeliver` / DLQ movement in NATS.

## Mitigation

- For publish failures, confirm the stream subject matches the kit-composed
  subject and that publisher credentials can write to that subject.
- For `too_large`, add an exact route override with
  `WithRouteMaxMessageBytes(exchange, routingKey, maxBytes)` only for the
  route that needs it.
- For `ack_failed`, `nak_failed`, or `term_failed`, treat duplicate delivery
  as possible. Stabilize broker connectivity before replaying or changing
  consumer state.
- For `handler_panic`, capture the payload ID and consumer logs before
  replay. The backend terminates panic-causing messages to avoid repeatedly
  burning the JetStream delivery budget.

## Metric Contract

- `nats_published_total{exchange,routing_key,outcome}`
- `nats_publish_duration_seconds{exchange,routing_key,outcome}`
- `nats_consumed_total{stream,durable,outcome}`
- `nats_handler_duration_seconds{stream,durable,outcome}`

Valid publish outcomes are `success`, `failed`, `invalid_message`, and
`too_large`. Valid consume outcomes are `acked`, `ack_failed`, `retry`,
`nak_failed`, `permanent`, `decode_error`, `handler_panic`, and `term_failed`.
Handler duration outcomes are `success`, `error`, and `panic`.
