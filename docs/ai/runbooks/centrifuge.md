# Realtime / Centrifuge Alerts

## Alerts

- `RhoKitCentrifugeConnectRejectHigh`
- `RhoKitCentrifugeConnectErrorRateHigh`

## What These Alerts Mean

`realtime/centrifuge` distinguishes three connect outcomes:

- `accepted` — auth passed, classifier returned, channel granted.
- `rejected` — auth refused (bad/expired JWT, policy refusal).
- `error` — internal failure (panic in classifier, JWT provider
  unreachable, centrifuge Node not started).

`rejected` is expected at a low rate (clients with stale tokens
will always exist). `error` should be near zero — every occurrence
is a kit / consumer-code bug, not a client-side issue.

## First Checks

1. Open the realtime centrifuge dashboard and filter to
   `namespace` and `service`.
2. Inspect the connects-by-outcome panel and the reject-ratio
   panel — is the rise smooth or stepwise?
3. Stepwise change → a deployment, a token-signing key rotation, or
   a clock-skew event on the issuer. Smooth rise → upstream client
   misbehavior (bot fleet, retry storm).
4. For `error` outcomes: check service logs for panics in the
   ChannelClassifier or JWTAuth provider; check that the
   centrifuge Node `Start` actually completed (the kit returns Stop
   as a no-op if Start never ran).

## Mitigation

- **JWT-rotation cause:** verify the issuer's signing keys are the
  ones the JWTAuth provider trusts. The kit's JWTAuth provider
  caches public keys; restart the consumer service if a hot
  rotation isn't being picked up.
- **Clock-skew cause:** check NTP on both issuer and consumer
  hosts. JWT `exp` / `nbf` validation has a 30s default leeway
  in `security/jwtutil`; clocks more than that apart will reject
  valid tokens.
- **Classifier panic:** the kit captures the panic and records
  `outcome="error"`, but the underlying call site is now broken.
  Fix the classifier and redeploy.
- **Stop-without-Start nil-deref:** centrifuge's Node panics on
  `Shutdown` if `Run` was never called. The kit guards against
  this (Stop is a no-op when not started), but if you see panics
  in logs around shutdown, confirm the kit version is at wave 164
  or later.

## Metric Contract

- `realtime_centrifuge_connects_total{outcome}` — counter, outcome
  ∈ `{accepted, rejected, error}`.
- `realtime_centrifuge_disconnects_total{reason}` — counter, reason
  ∈ `{clean, stale}` (clean=client-initiated, stale=server kicked).
- `realtime_centrifuge_subscribes_total{class}` — counter, `class`
  is the operator-defined channel class from `WithChannelClassifier`
  (e.g. `"user"`, `"room"`, `"system"`).
- `realtime_centrifuge_publishes_total{class}` — counter, same
  class label.

The `class` label is projected through
`promutil.OpaqueLabelValue` as a cardinality safety net — a
classifier that accidentally returns a per-tenant UUID will collapse
to an opaque hash rather than inflating the label set.
