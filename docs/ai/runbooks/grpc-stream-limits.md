# gRPC Stream-Limit Alerts

## Alerts

- `RhoKitGRPCStreamCapacityRejecting`
- `RhoKitGRPCStreamIdleClosesSpike`

## What These Alerts Mean

`grpcx/interceptor` adds two server-wide stream resource controls
on top of gRPC's per-connection caps:

- `MaxConcurrentStreamsServer(max, metrics)` caps the number of
  streaming RPCs across ALL client connections. Beyond the cap,
  new streams are rejected with `codes.ResourceExhausted` before
  the handler runs.
- `StreamIdleTimeout(d, metrics)` cancels streams that have neither
  sent nor received a message for `d`. gRPC's HTTP/2 keepalive
  detects DEAD peers but not IDLE streams that simply stop talking.

`grpc_server_streams_rejected_total{reason="max_concurrent"}`
ticking means the server-wide cap is binding. Either the cap is
too low for current load OR a handler is leaking streams (returning
without releasing — usually impossible since the kit's interceptor
holds the slot via defer, but a panic in middleware further down
the chain could still corrupt the counter).

`grpc_server_streams_idle_closed_total` ticking means the watchdog
fired. Streams ARE the appropriate thing to close when clients
pause indefinitely; the alert flags abnormal spikes, not the
baseline.

## First Checks

1. Open the gRPC stream-limits dashboard and filter to the
   alerting `namespace` and `service`.
2. Plot `grpc_server_active_streams` over the last 6 hours — is
   it climbing without a corresponding handled-total rate?
3. Cross-check `grpc_server_handled_total{grpc_type=~"server_stream|bidi_stream"}`
   rate — are streams completing at all?
4. Inspect client deployment timeline — did a new client version
   roll out around the spike?

## Mitigation

- **Capacity rejecting and load is genuine:** raise the
  `MaxConcurrentStreamsServer` cap. Validate first that the
  process has the goroutine / fd budget for the higher cap.
- **Capacity rejecting with no traffic increase:** stream leak.
  Diff handlers added since the last clean baseline; look for
  goroutines that are spawned in the handler and outlive the
  handler return.
- **Idle closes spike:** correlate with client deployments.
  Mobile-heavy workloads see this naturally (apps backgrounded).
  Web-heavy workloads usually don't unless a TCP-level proxy
  changed. If the spike is real-user pain (not just bookkeeping),
  raise `StreamIdleTimeout` to the 95th percentile of legitimate
  idle gaps measured during normal operation.
- **Capacity at zero but active climbs:** the rejected counter
  isn't ticking but active is. This is the leak shape — confirm
  with a goroutine dump and look for goroutines parked on
  `(*grpc.serverStream).RecvMsg`.

## Cap Sizing Guidance

The right value depends on the per-stream goroutine cost and the
host's open-fd limit. A common starting point: 4× the steady-state
peak observed during a successful load test. Too low and legitimate
bursts get rejected; too high and the protection is theatrical.

## Metric Contract

- `grpc_server_active_streams` — gauge, currently-open streaming
  RPCs. Server-wide; not per-connection.
- `grpc_server_streams_rejected_total{reason}` — counter, `reason`
  is a bounded enum (currently only `max_concurrent`).
- `grpc_server_streams_idle_closed_total` — counter, ticks once
  per kit-initiated idle-timeout cancellation.

The cap interceptor and idle-timeout interceptor share the same
`StreamLimitMetrics` value (or run with `nil` metrics for a silent
no-op).
