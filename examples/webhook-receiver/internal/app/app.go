// Package app wires the webhook-receiver EXAMPLE.
//
// Composition shown:
//
//	signedrequest.Middleware  (HMAC-SHA256 verification)
//	  → idempotency.Middleware (cache for ID'd retries)
//	    → typed JSON handler
//
// The wiring order is important: signature verification MUST run
// before idempotency, because:
//
//   - A forged request with a valid idempotency key would otherwise
//     poison the cache.
//   - Signature verification needs the raw body; idempotency reads
//     the body to compute the fingerprint. signedrequest consumes
//     the body first, then restores it as a bytes.Buffer so
//     idempotency can read it again.
//
// SECURITY: this example uses in-memory backends so it stands up
// without external dependencies. Production deployments swap:
//
//   - signedrequest.NewMemoryNonceStore → a Redis-backed
//     implementation (the nonce window is short, but cross-replica
//     visibility matters);
//   - idem.NewMemoryStore → idempotency/redisstore.New or
//     idempotency/pgstore.New. The kit-doctor rule
//     `idempotency-memory-store` flags the example's in-memory use;
//     here we tolerate it because the example is single-process.
//   - the static KeyResolver → a per-tenant lookup against a secret
//     manager (Vault, AWS Secrets Manager, GCP Secret Manager).
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	idem "github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

const (
	hmacKeyEnv      = "WEBHOOK_HMAC_KEY"
	minHMACKeyChars = 32
	defaultAddr     = ":8090"
)

// Run starts the webhook receiver on :8090 (or the test address
// passed via [run]). Blocks until ctx is cancelled.
func Run(ctx context.Context) error {
	return run(ctx, defaultAddr)
}

func run(ctx context.Context, addr string) error {
	rawKey, err := hmacKeyFromEnv()
	if err != nil {
		return err
	}
	receiver := newReceiver()

	mux := http.NewServeMux()
	mux.Handle("POST /webhook", buildWebhookHandler(rawKey, receiver))
	mux.Handle("GET /received", receiver.listHandler())

	srv := httpx.NewServer(addr, mux, httpx.WithErrorLog(
		slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	))
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Default().Info("webhook-receiver listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// buildWebhookHandler wires the canonical signedrequest →
// idempotency → typed-handler chain.
func buildWebhookHandler(hmacKey []byte, r *receiver) http.Handler {
	keyResolver := func(_ context.Context, keyID string) ([]byte, error) {
		if keyID != "demo-tenant" {
			return nil, errors.New("unknown key id")
		}
		return hmacKey, nil
	}
	nonceStore := signedrequest.NewMemoryNonceStore(5 * time.Minute)
	verify := signedrequest.Middleware(keyResolver, nonceStore,
		signedrequest.WithBodyMaxSize(1<<20 /* 1 MiB */),
	)

	// Example service, single-process, no cross-replica replay to defend against.
	// kit-doctor:allow idempotency-memory-store
	store := idem.NewMemoryStore()
	cache := idempotency.Middleware(store,
		idempotency.WithAllowSharedKeys(),
		idempotency.WithTTL(10*time.Minute),
	)

	return verify(cache(http.HandlerFunc(r.handleWebhook)))
}

// receiver holds the demo's tiny in-memory record of accepted events.
// In a real service this would publish into outbox / messaging /
// downstream worker queue.
type receiver struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Payload    string    `json:"payload"`
	ReceivedAt time.Time `json:"received_at"`
}

type webhookRequest struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Payload string `json:"payload"`
}

func newReceiver() *receiver { return &receiver{} }

func (r *receiver) handleWebhook(w http.ResponseWriter, req *http.Request) {
	var body webhookRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.ID == "" || body.Kind == "" {
		http.Error(w, "id and kind are required", http.StatusBadRequest)
		return
	}
	event := recordedEvent{
		ID:         body.ID,
		Kind:       body.Kind,
		Payload:    body.Payload,
		ReceivedAt: time.Now().UTC(),
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":      "accepted",
		"event_id":    body.ID,
		"received_at": event.ReceivedAt.Format(time.RFC3339Nano),
	})
}

func (r *receiver) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		out := make([]recordedEvent, len(r.events))
		copy(out, r.events)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func hmacKeyFromEnv() ([]byte, error) {
	val := os.Getenv(hmacKeyEnv)
	if len(val) < minHMACKeyChars {
		return nil, fmt.Errorf("%s must be at least %d characters (got %d) — generate with: openssl rand -hex 32", hmacKeyEnv, minHMACKeyChars, len(val))
	}
	return []byte(val), nil
}

// listenPortHint extracts the TCP port from a "host:port" listen
// address so the smoke test can report the bound port back to the
// log. Unused by Run but exported for the test helper.
func listenPortHint(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	var p int
	if _, err := fmt.Sscanf(portStr, "%d", &p); err != nil {
		return 0, err
	}
	return p, nil
}
