# examples/webhook-receiver

> **SECURITY**: this is an EXAMPLE for learning the canonical
> rho-kit webhook-ingest pattern. The binary requires a strong
> HMAC key to start, but uses in-memory backends for nonce
> tracking and idempotency cache. Production deployments swap
> both stores and the static key resolver for managed equivalents
> (see "What's NOT in this example" below).

A reference rho-kit v2.0.0 service that demonstrates the canonical
webhook-ingest composition:

```
signedrequest.Middleware  →  idempotency.Middleware  →  typed JSON handler
       (verify HMAC)               (cache 202s)              (record event)
```

The wiring order is load-bearing:

1. **`signedrequest.Middleware`** consumes the raw body to verify
   the HMAC-SHA256 signature against the canonical
   `(method, URI, host, content-type, ts, nonce, body-hash)`
   string. It restores the body via an in-memory buffer (capped at
   `WithInMemoryBodyMax`) so downstream middleware can re-read it.
2. **`idempotency.Middleware`** keys on the `Idempotency-Key`
   header and the request body fingerprint. A retry from the
   upstream with the same key returns the cached 202 without
   re-invoking the handler. This protects against duplicate
   side-effects.
3. **The typed handler** decodes JSON into `webhookRequest` and
   records the event.

Signature verification MUST run BEFORE idempotency: a forged
request with a valid Idempotency-Key would otherwise poison the
cache.

## Run

```bash
export WEBHOOK_HMAC_KEY="$(openssl rand -hex 32)"
go run ./cmd/webhook-receiver
# Listens on :8090
```

## Exercise it

The kit's canonical signing scheme is:

```
canonical = method + "\n"
          + request-uri + "\n"
          + lowercase(host) + "\n"
          + content-type + "\n"
          + unix-timestamp + "\n"
          + nonce + "\n"
          + hex(sha256(body))
signature = "hmac-sha256=" + base64.StdEncoding(hmac-sha256(key, canonical))
```

Headers required on every signed request:

- `X-Signature-Timestamp` — Unix seconds.
- `X-Signature-Nonce` — base64-RawURL-encoded 16 random bytes (22 chars).
- `X-Signature-Key-Id` — opaque key identifier; this example uses
  `demo-tenant` as the only valid id.
- `X-Signature` — `hmac-sha256=<padded-base64-MAC>`.

A real publisher would use the kit's
`httpx/middleware/signedrequest/signer` helper. For local poking,
`internal/app/app_test.go::newSignedRequest` shows the exact wire
format used in the smoke tests.

Inspect what was accepted:

```bash
curl -s http://localhost:8090/received | jq
```

## What's NOT in this example

- **Persistent nonce store**: `signedrequest.NewMemoryNonceStore`
  evaporates on restart. Production swaps a Redis-backed store so
  replay protection survives both restarts and replica scale-out.
- **Persistent idempotency cache**: the example tolerates
  `idempotency.NewMemoryStore` (single-process). Multi-replica
  services use `data/idempotency/redisstore.New` or
  `data/idempotency/pgstore.New`; `kit-doctor` flags the
  in-memory store outside tests.
- **Per-tenant key resolution**: the demo's `KeyResolver` returns
  one static key for one keyID. Production fetches per-tenant
  keys from a secret manager and rotates them.
- **Downstream dispatch**: this example writes accepted events
  to an in-memory slice. A real service publishes into the
  outbox (`infra/outbox`) so downstream messaging is crash-safe.
- **Observability**: production wiring adds the kit's
  `signedrequest.WithMetrics` and registers OTel tracing on the
  handler chain.

## Smoke tests

```bash
go test ./examples/webhook-receiver/...
```

The tests cover:
- Happy path: HMAC-signed POST with `Idempotency-Key` → 202.
- Unsigned POST is rejected before reaching the cache.
- Idempotent retry with the same key returns the cached 202
  without double-recording.
