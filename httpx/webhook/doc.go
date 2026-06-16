// Package webhook is the outbound counterpart to
// [httpx/middleware/signedrequest] (which RECEIVES signed webhooks).
// Use it to SEND HMAC-signed HTTP requests to third-party endpoints
// with retry on transient failures and replay-protection nonces.
//
// # Use this package when
//
//   - Your service emits webhooks to customer endpoints (Stripe-style
//     event delivery, Slack-incoming-webhook style, partner callbacks).
//   - You want retry-with-backoff baked in so a momentary 503 doesn't
//     drop the event.
//
// # Do NOT use this package for
//
//   - Receiving webhooks. Use [httpx/middleware/signedrequest] instead.
//   - In-process events. Use [runtime/eventbus] (faster, no HTTP).
//   - Cross-service messaging with at-least-once guarantees through
//     a broker. Use [infra/messaging] or [infra/outbox] — webhooks
//     are point-to-point HTTP, not message-bus.
//
// # Sibling packages
//
//   - [crypto/signing]                    — the HMAC signer this
//     package uses; configure shared secrets there.
//   - [httpx/middleware/signedrequest]   — receiver side.
//   - [resilience/retry]                 — the retry primitive the
//     dispatcher wraps for transient failures.
//
// # Quick start
//
//	signer := signing.NewSigner()
//	d, err := webhook.New(webhook.Config{
//	    HTTPClient: httpx.NewResilientHTTPClient(),
//	    Signer:     signer,
//	    Secret:     []byte(os.Getenv("WEBHOOK_SECRET")),
//	})
//	if err != nil {
//	    return err // misconfiguration: nil client/signer, or secret < 32 bytes
//	}
//
//	err = d.Send(ctx, webhook.Delivery{
//	    URL:         "https://customer.example.com/webhooks/orders",
//	    Body:        json.RawMessage(`{"event":"order.created","id":"o-42"}`),
//	    ContentType: "application/json",
//	})
package webhook
