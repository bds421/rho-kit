# httpx/webhook

## Purpose

Outbound HMAC-signed HTTP webhook dispatcher with retry. Counterpart
to `httpx/middleware/signedrequest` (receiver).

## Public API

- `New(cfg Config, opts ...Option) (*Dispatcher, error)`
- `Dispatcher.Send(ctx, Delivery) error`
- `Delivery{URL, Body, ContentType, Headers, DeliveryID}`
- Options: `WithLogger`, `WithSignatureHeader`, `WithTimestampHeader`,
  `WithIDHeader`, `WithRetryPolicy`
- Defaults: signature `X-Kit-Signature`, timestamp `X-Kit-Timestamp`,
  delivery-id `X-Kit-Delivery-Id`. Receivers wired with
  `httpx/middleware/signedrequest` expect this triplet.

## Retry semantics

- Transport error → retryable
- HTTP 5xx → retryable
- HTTP 4xx → permanent (receiver said "your fault — don't retry")
- HTTP 2xx → success
- ctx cancellation → halts loop immediately

`WithRetryPolicy(retry.DefaultPolicy())` is the default (3 attempts,
1s base, 30s cap, 2x factor, ±25% jitter, RetryIfNotPermanent).

## Security

- HMAC-SHA256 over the body via `crypto/signing`. The same Signer
  configured for receiving can be used here for round-trip self-tests.
- Delivery-ID auto-generated (UUIDv7) if omitted. Receivers should
  store the ID in a nonce store to block replay (see
  `httpx/middleware/signedrequest.NewMemoryNonceStore`).
- Kit headers (X-Kit-Signature / Timestamp / Delivery-Id) OVERWRITE
  caller-supplied entries with the same name, so a misconfigured
  caller can't accidentally suppress the signature.
- Default `CheckRedirect` (installed by `New` when the client has none)
  refuses redirects to non-https or private/reserved nets via
  `security/netutil.ResolveAndValidate`. Explicit client policies
  (e.g. `httpx.NewResilientHTTPClient`) are never overwritten. Still
  wire an SSRF-aware transport for untrusted destinations.

## Tests

`go test -race ./...`. Covers:

- Happy path with signature + timestamp + auto delivery-id
- Default Content-Type is application/json
- Caller-supplied DeliveryID preserved
- Retries on 5xx then succeeds
- 4xx gives up immediately (no retry)
- Network error retries
- Ctx cancel halts loop
- Config validation (required HTTPClient/Signer/Secret)
- Nil option rejected
- URL required
- Custom headers DO NOT override kit headers

## See also

- `httpx/middleware/signedrequest` — receiver side. Use the same
  Signer + Secret for self-tests.
- `crypto/signing` — the HMAC primitive.
- `resilience/retry` — the retry policy machinery.
- `infra/outbox` — durable asynchronous dispatch when delivery must
  survive process crashes; `Dispatcher.Send` remains the synchronous path.
