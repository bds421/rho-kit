// Package app wires the saga-coordinator EXAMPLE.
//
// Composition shown:
//
//	HTTP handler
//	  → exclusive section (in-process mutex per Idempotency-Key)
//	    → idempotency cache lookup → return cached on hit
//	    → saga.Run(definition, state)
//	         step 1: reserve-inventory (Forward + Compensate)
//	         step 2: charge-card       (Forward + Compensate)
//	         step 3: ship              (Forward + Compensate)
//	      ↳ on failure: kit auto-runs Compensate on prior steps
//	                    in reverse order
//	    → cache the successful result under the Idempotency-Key
//
// Why the three primitives compose this way:
//
//   - **saga.Run** owns the forward/compensate orchestration. If
//     step N fails, the kit invokes Compensate on steps N-1 ... 0
//     in reverse, then returns a *saga.ForwardError joined with
//     a *saga.CompensateError when any compensation also failed.
//
//   - **idempotency** wraps the saga so retries from the same
//     caller (same Idempotency-Key) return the cached result.
//     Without it, a network-level retry would re-run every step
//     — even if Forward callbacks are idempotent individually, the
//     side-effects compound (two inventory holds, two charges).
//
//   - **exclusive section** (mutex here; pgadvisory.Acquire in
//     production) prevents concurrent retries from racing each
//     other into the same idempotency window. The saga itself
//     is sequential; the lock prevents two callers from BOTH
//     starting before either's Set hits the cache.
//
// SECURITY: this example uses in-memory backends. Production:
//   - swaps `idem.NewMemoryStore` for
//     `data/idempotency/pgstore.New` or `redisstore.New`
//     (kit-doctor's `idempotency-memory-store` rule flags the
//     example's in-memory use — suppressed inline);
//   - swaps the in-process `sync.Mutex` for
//     `data/lock/pgadvisory.AcquireTx` so the exclusive section
//     survives replica failover and pins to the actual database
//     transaction that the saga's side-effects write to;
//   - wraps each saga step with `infra/outbox.Publish` rather
//     than calling downstream services directly, so the kit's
//     wave-149 Multiplex relay handles crash-safe delivery.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	apphttp "github.com/bds421/rho-kit/app/http/v2"
	"github.com/bds421/rho-kit/app/v2"
	idem "github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

const (
	idempotencyTTL = 1 * time.Hour
	idempotencyHdr = "Idempotency-Key"
)

// OrderRequest is the typed payload accepted by POST /orders. The
// saga's state is a pointer to OrderState so each step can read
// the request and append to the audit trail.
type OrderRequest struct {
	OrderID  string  `json:"order_id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// OrderState is the in-flight state threaded through the saga.
// Steps mutate it through pointer; the kit's saga package is
// type-agnostic and hands the same `any` to every callback.
type OrderState struct {
	Request      OrderRequest
	ReservedQty  int
	ChargeID     string
	ShipmentID   string
	StepsApplied []string
}

// Run boots the saga coordinator via app.Builder so the example
// demonstrates the canonical production wiring. Builder runs the
// always-on validator at startup; the example opts out per-policy
// (apphttp.WithoutTLS, WithoutRateLimit) for curl/test convenience.
// kit-doctor flags each opt-out in production code.
func Run(ctx context.Context) error {
	logger := slog.Default()
	coord := newCoordinator(realInventoryReserve, realCardCharge, realShipmentDispatch)

	cfg := app.BaseConfig{
		Server:      app.ServerConfig{Host: "127.0.0.1", Port: 8097},
		Internal:    app.InternalConfig{Host: "127.0.0.1", Port: 9097},
		Environment: "example",
		LogLevel:    "info",
	}
	return app.New("saga-coordinator", "0.0.0-example", cfg).
		Logger(logger).
		WithoutRateLimit().
		// Example listens on plain http for curl/test convenience.
		// kit-doctor:allow apphttp-without-tls
		With(apphttp.Module(apphttp.WithoutTLS())).
		Router(func(_ app.Infrastructure) http.Handler {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /orders", coord.handleOrder)
			mux.HandleFunc("GET /orders", coord.handleListOrders)
			return mux
		}).
		RunContext(ctx)
}

// coordinator groups the saga definition with the idempotency
// cache and the per-key exclusive section. Step callables are
// injected so the smoke test can substitute deterministic
// failure shapes.
type coordinator struct {
	// Example service, single-process, no cross-replica replay to defend against.
	// kit-doctor:allow idempotency-memory-store
	store     *idem.MemoryStore
	keyMu     keyedMutex
	steps     stepBundle
	completed sync.Map // map[orderID]*OrderState — read-only view
}

// stepBundle isolates the Forward callables so tests can swap
// individual steps with failure injections.
type stepBundle struct {
	reserveInventory func(ctx context.Context, s *OrderState) error
	chargeCard       func(ctx context.Context, s *OrderState) error
	dispatchShipment func(ctx context.Context, s *OrderState) error
}

func newCoordinator(reserve, charge, ship func(context.Context, *OrderState) error) *coordinator {
	return &coordinator{
		// Example service, single-process, no cross-replica replay to defend against.
		// kit-doctor:allow idempotency-memory-store
		store: idem.NewMemoryStore(),
		steps: stepBundle{
			reserveInventory: reserve,
			chargeCard:       charge,
			dispatchShipment: ship,
		},
	}
}

// buildDefinition assembles the canonical 3-step Definition. The
// closures bind the coordinator's injected callables; compensation
// callbacks are inlined here because they're tightly coupled to
// what Forward did.
func (c *coordinator) buildDefinition() *saga.Definition {
	return saga.MustDefinition(
		saga.Step{
			Name: "reserve-inventory",
			Forward: func(ctx context.Context, state any) error {
				s := state.(*OrderState)
				if err := c.steps.reserveInventory(ctx, s); err != nil {
					return err
				}
				s.StepsApplied = append(s.StepsApplied, "reserve-inventory")
				return nil
			},
			Compensate: func(_ context.Context, state any) error {
				s := state.(*OrderState)
				s.ReservedQty = 0
				s.StepsApplied = append(s.StepsApplied, "release-inventory")
				return nil
			},
		},
		saga.Step{
			Name: "charge-card",
			Forward: func(ctx context.Context, state any) error {
				s := state.(*OrderState)
				if err := c.steps.chargeCard(ctx, s); err != nil {
					return err
				}
				s.StepsApplied = append(s.StepsApplied, "charge-card")
				return nil
			},
			Compensate: func(_ context.Context, state any) error {
				s := state.(*OrderState)
				s.ChargeID = ""
				s.StepsApplied = append(s.StepsApplied, "refund-card")
				return nil
			},
		},
		saga.Step{
			Name: "dispatch-shipment",
			Forward: func(ctx context.Context, state any) error {
				s := state.(*OrderState)
				if err := c.steps.dispatchShipment(ctx, s); err != nil {
					return err
				}
				s.StepsApplied = append(s.StepsApplied, "dispatch-shipment")
				return nil
			},
			Compensate: func(_ context.Context, state any) error {
				s := state.(*OrderState)
				s.ShipmentID = ""
				s.StepsApplied = append(s.StepsApplied, "cancel-shipment")
				return nil
			},
		},
	)
}

// runSaga is the composition seam: exclusive section → idempotency
// cache → saga.Run → cache write. The smoke test exercises this
// directly so it doesn't have to drive HTTP for every assertion.
func (c *coordinator) runSaga(ctx context.Context, idemKey string, req OrderRequest) (*OrderState, error) {
	// Exclusive section: serialize concurrent retries with the same
	// idempotency key. Production wires pgadvisory.Acquire here.
	release := c.keyMu.Lock(idemKey)
	defer release()

	// Idempotency cache hit → return the cached state without
	// re-running any step.
	if cached, ok := c.lookupCache(ctx, idemKey); ok {
		return cached, nil
	}

	state := &OrderState{Request: req}
	def := c.buildDefinition()
	if err := saga.Run(ctx, def, state); err != nil {
		// Saga failed AND compensated. Do NOT cache the failure —
		// a future retry should try again (the upstream sender may
		// have a fix in place).
		return state, err
	}
	c.storeCache(ctx, idemKey, state)
	c.completed.Store(req.OrderID, state)
	return state, nil
}

func (c *coordinator) lookupCache(ctx context.Context, idemKey string) (*OrderState, bool) {
	// MemoryStore stores raw bytes; we encode the OrderState as JSON
	// so the cache contract matches the production redisstore/pgstore.
	//
	// Get's `ok` return signals "fingerprint mismatch" (the key exists
	// but the request body doesn't match the cached entry's fingerprint).
	// A cache HIT is `(resp != nil, false, nil)`; a cache MISS is
	// `(nil, false, nil)`. This example does not use the fingerprint
	// channel, so we ignore `ok` entirely and branch on `resp != nil`.
	resp, _, err := c.store.Get(ctx, idemKey, nil)
	if err != nil || resp == nil {
		return nil, false
	}
	var state OrderState
	if err := json.Unmarshal(resp.Body, &state); err != nil {
		return nil, false
	}
	return &state, true
}

func (c *coordinator) storeCache(ctx context.Context, idemKey string, state *OrderState) {
	body, err := json.Marshal(state)
	if err != nil {
		return
	}
	// TryLock returns (token, fingerprintMismatch, acquired, err).
	// Cache the response only when we successfully acquired the lock.
	token, _, acquired, err := c.store.TryLock(ctx, idemKey, nil, idempotencyTTL)
	if err != nil || !acquired {
		return
	}
	_ = c.store.Set(ctx, idemKey, token, idem.CachedResponse{
		StatusCode: http.StatusOK,
		Body:       body,
	}, idempotencyTTL)
}

// HTTP wiring ------------------------------------------------------

func (c *coordinator) handleOrder(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get(idempotencyHdr)
	if idemKey == "" {
		http.Error(w, idempotencyHdr+" header is required", http.StatusBadRequest)
		return
	}
	// Reject keys the store would silently refuse (whitespace,
	// control chars, over-length). Without this, store.Get/TryLock
	// error out, the cache treats the request as a miss, and the
	// saga re-executes on every retry — the double-charge the
	// idempotency layer exists to prevent.
	if err := idem.ValidateKey(idemKey); err != nil {
		http.Error(w, idempotencyHdr+" header is invalid", http.StatusBadRequest)
		return
	}
	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.OrderID == "" || req.Amount <= 0 {
		http.Error(w, "order_id and positive amount are required", http.StatusBadRequest)
		return
	}

	state, err := c.runSaga(r.Context(), idemKey, req)
	if err != nil {
		writeSagaError(w, state, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func (c *coordinator) handleListOrders(w http.ResponseWriter, _ *http.Request) {
	var out []*OrderState
	c.completed.Range(func(_, v any) bool {
		out = append(out, v.(*OrderState))
		return true
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// writeSagaError distinguishes compensation-completed (422,
// "rolled back cleanly") from compensation-partially-failed (500,
// "manual intervention required"). The kit's saga package joins
// *ForwardError + *CompensateError in the latter case; we surface
// the distinction so operators can route incidents correctly.
func writeSagaError(w http.ResponseWriter, state *OrderState, err error) {
	var compErr *saga.CompensateError
	if errors.As(err, &compErr) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":         "saga rolled back with partial compensation failure",
			"state":         state,
			"compensations": compErr.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": err.Error(),
		"state": state,
	})
}

// keyedMutex grants exclusive access keyed by a string. The
// production replacement is data/lock/pgadvisory.AcquireTx, which
// pins exclusion to the database transaction the saga's side-
// effects write to.
//
// Each per-key entry is reference-counted by the number of
// goroutines that have entered Lock but not yet released. The
// entry is deleted once the last holder/waiter releases, so the
// holders map cannot grow without bound under attacker-controlled
// (unique-per-request) Idempotency-Key headers.
type keyedMutex struct {
	mu      sync.Mutex
	holders map[string]*keyedMutexEntry
}

// keyedMutexEntry pairs a per-key lock with a reference count of
// the goroutines currently using it (holder + waiters). refs is
// guarded by the parent keyedMutex.mu, not by mu.
type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.holders == nil {
		k.holders = make(map[string]*keyedMutexEntry)
	}
	e, ok := k.holders[key]
	if !ok {
		e = &keyedMutexEntry{}
		k.holders[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()

	var released bool
	return func() {
		// Guard against a double release decrementing refs twice,
		// which would corrupt the cleanup accounting.
		if released {
			return
		}
		released = true
		e.mu.Unlock()

		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.holders, key)
		}
		k.mu.Unlock()
	}
}

// Real step callables ---------------------------------------------

// realInventoryReserve is the production stand-in. In a real saga
// this would call an inventory service or a database transaction.
func realInventoryReserve(_ context.Context, s *OrderState) error {
	s.ReservedQty = 1
	return nil
}

// realCardCharge is the production stand-in for a payment-provider
// call (Stripe charge, internal billing service, etc.).
func realCardCharge(_ context.Context, s *OrderState) error {
	s.ChargeID = "ch_" + s.Request.OrderID
	return nil
}

// realShipmentDispatch is the production stand-in for a shipping-
// provider call.
func realShipmentDispatch(_ context.Context, s *OrderState) error {
	s.ShipmentID = "shp_" + s.Request.OrderID
	return nil
}
