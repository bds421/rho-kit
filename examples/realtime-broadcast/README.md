# examples/realtime-broadcast

> **SECURITY**: this is an EXAMPLE for learning the rho-kit
> browser-facing real-time composition. The binary collapses the
> issuer and verifier into one process: it generates an ECDSA
> keypair at startup, signs demo tokens with the private key, and
> exposes the corresponding JWKS at `/demo/jwks`. Production
> deployments NEVER do this — the broadcast server holds only
> the public-half JWKS, fetched from a real issuer (Auth0,
> Cognito, Keycloak, custom IDP) via `jwtutil.NewProvider(jwksURL,
> ...)`. The collapsed example exists for pedagogy and a smoke
> test that runs without an external IDP.

A reference rho-kit v2.0.0 service that demonstrates the canonical
browser-facing real-time composition:

```
httpx.NewServer
  ↳ /connection/websocket  →  centrifuge.Node.WebsocketHandler
                              (centrifuge.NewNode wired with
                               WithJWTAuth(verifier) +
                               WithChannelClassifier(...))
  ↳ /demo/token            →  debug endpoint that signs a JWT
  ↳ /demo/jwks             →  public-key JWKS the verifier reads
```

The kit's contributions are:

1. **`centrifuge.NewNode`** wraps the upstream centrifuge library
   and exposes it as a `lifecycle.Component` (Start/Stop) so it
   composes with `runtime/lifecycle.Runner` alongside the HTTP
   server.
2. **`WithJWTAuth`** wires connect-time JWT verification via the
   kit's `jwtutil.Provider` — issuer, audience, signature, and
   expiry are all validated. `kit-doctor`'s
   `centrifuge-missing-jwt-auth` rule (wave 173, CRITICAL) flags
   any `centrifuge.NewNode` call that omits this — an
   unauthenticated realtime endpoint is a data-exfiltration
   channel for any in-process state the service broadcasts.
3. **`WithChannelClassifier`** projects channel names through a
   bounded-cardinality "class" label. A well-behaved classifier
   returns a fixed enum like `user` / `room` / `system` — the
   kit additionally projects every value through
   `promutil.OpaqueLabelValue` as a safety net so a misbehaving
   classifier cannot inflate Prometheus cardinality.

## Run

```bash
go run ./cmd/realtime-broadcast
# Listens on :8096; centrifuge websocket at /connection/websocket
# Token endpoint at /demo/token
# JWKS endpoint at /demo/jwks
```

## Exercise it

```bash
# 1. Get a signed token for a demo subject.
TOKEN=$(curl -s "http://localhost:8096/demo/token?sub=alice" | jq -r .token)

# 2. Inspect the JWKS the server uses to verify it.
curl -s http://localhost:8096/demo/jwks | jq

# 3. Connect with a centrifuge JS / Go client, passing $TOKEN as
#    the connect token. The kit will validate iss/aud/sig/exp
#    before the OnConnecting callback completes.
#
#    Example using the centrifugo CLI:
#      centrifugo connect --token "$TOKEN" \
#        ws://localhost:8096/connection/websocket
```

## Smoke tests

```bash
go test ./examples/realtime-broadcast/...
```

The tests cover:
- Issuer ↔ verifier round-trip: a token signed by the demo private
  key verifies against the JWKS the same key exposes.
- Audience mismatch is rejected (RFC 7519 §4.1.3 confused-deputy
  mitigation).
- Expired token is rejected (kit takes an explicit `now` so the
  test fast-forwards without sleeping).
- `centrifuge.NewNode(WithJWTAuth, WithChannelClassifier)` builds
  cleanly and `Stop` is safe when `Start` never reached (wave-164
  nil-deref guard).
- `classifyChannel` mapping is pinned: `user:` / `room:` /
  `system:` / unknown → `default`.
- `/demo/token` endpoint issues a token the verifier accepts.
- `/demo/jwks` endpoint exposes parseable JWKS that an
  independently-built verifier can ingest.

The full websocket protocol round-trip is intentionally NOT
exercised — that would require the centrifuge client library
and would test the upstream library rather than the kit's
composition. The smoke test surface validates every kit-side
contribution: JWT validation, channel classification, lifecycle
hooks, and the wire shape exposed to clients.

## Production wiring

For a real IDP, replace `newSigningKey` + `newVerifier` with:

```go
import (
    "github.com/bds421/rho-kit/security/v2/jwtutil"
)

httpClient := httpx.NewHTTPClient(httpx.WithClientTimeout(5*time.Second))
verifier := jwtutil.NewProvider(
    "https://your-idp.example.com/.well-known/jwks.json",
    httpClient,
    1*time.Hour, // refresh interval
    jwtutil.WithExpectedIssuer("https://your-idp.example.com"),
    jwtutil.WithExpectedAudience("https://realtime.example.com"),
)
go verifier.Run(ctx) // background JWKS refresh

node, _ := centrifuge.NewNode(
    centrifuge.WithJWTAuth(verifier),
    centrifuge.WithChannelClassifier(classifyChannel),
)
```

Then drop the `/demo/token` and `/demo/jwks` handlers — those
exist only because this example self-signs.

## What's NOT in this example

- **Real IDP integration.** The example collapses issuer +
  verifier; production fetches JWKS from a real issuer.
- **TLS termination.** The example listens on plain `http://`;
  production runs behind TLS so the websocket upgrade is `wss://`
  and tokens never travel in clear.
- **Centrifuge presence/history backends.** The example uses
  centrifuge's in-process defaults. Production wires Redis or
  another shared store via the centrifuge engine option so
  multiple replicas share presence state.
- **Per-channel authorization.** The example accepts any
  authenticated client on any channel. Production wires
  `node.Underlying().OnSubscribe` to consult an authorization
  service before allowing subscribe/publish.
- **Observability dashboards.** Already shipped in wave 172:
  `observability/dashboards/grafana/centrifuge.json` and the
  `RhoKitCentrifugeConnect*` alerts under
  `alerts-coordination.yaml`. The runbook lives at
  `docs/ai/runbooks/centrifuge.md`.
