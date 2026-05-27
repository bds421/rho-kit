package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/runtime/v2/saga"
)

// TestRunSaga_HappyPath drives the canonical end-to-end: three
// real step callables run in order, every step succeeds, the
// returned state reflects every Forward action having been
// applied.
func TestRunSaga_HappyPath(t *testing.T) {
	c := newCoordinator(realInventoryReserve, realCardCharge, realShipmentDispatch)
	state, err := c.runSaga(context.Background(), "idem-1", OrderRequest{
		OrderID:  "ord-1",
		Amount:   42.5,
		Currency: "USD",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, state.ReservedQty)
	assert.Equal(t, "ch_ord-1", state.ChargeID)
	assert.Equal(t, "shp_ord-1", state.ShipmentID)
	assert.Equal(t, []string{"reserve-inventory", "charge-card", "dispatch-shipment"}, state.StepsApplied)
}

// TestRunSaga_CompensationOnStep3Failure exercises the kit's
// rollback semantics: step 3 fails, kit invokes Compensate on
// steps 2 then 1 in reverse order. The returned state shows the
// audit trail of forward + compensate operations.
func TestRunSaga_CompensationOnStep3Failure(t *testing.T) {
	failing := &failOnce{}
	c := newCoordinator(realInventoryReserve, realCardCharge, failing.fail)
	state, err := c.runSaga(context.Background(), "idem-fail", OrderRequest{
		OrderID: "ord-fail",
		Amount:  10,
	})
	require.Error(t, err)

	var fwd *saga.ForwardError
	require.True(t, errors.As(err, &fwd), "kit must surface *saga.ForwardError")
	assert.Equal(t, "dispatch-shipment", fwd.Name)
	assert.Equal(t, int32(1), failing.calls.Load(), "failing step ran exactly once")

	// Audit trail: forward steps 1+2 ran, then compensation ran in
	// reverse (refund-card, then release-inventory).
	assert.Equal(t,
		[]string{"reserve-inventory", "charge-card", "refund-card", "release-inventory"},
		state.StepsApplied,
	)
	// Compensate cleared the side-effects.
	assert.Equal(t, 0, state.ReservedQty)
	assert.Empty(t, state.ChargeID)
}

// TestRunSaga_CompensationOnStep2Failure pins the partial-rollback
// case: step 2 fails, only step 1 needs compensation. The
// failing step itself is NOT compensated (its forward returned
// an error, so there's nothing to undo).
func TestRunSaga_CompensationOnStep2Failure(t *testing.T) {
	failing := &failOnce{}
	c := newCoordinator(realInventoryReserve, failing.fail, realShipmentDispatch)
	state, err := c.runSaga(context.Background(), "idem-fail-mid", OrderRequest{
		OrderID: "ord-mid",
		Amount:  10,
	})
	require.Error(t, err)
	assert.Equal(t, []string{"reserve-inventory", "release-inventory"}, state.StepsApplied)
	assert.Empty(t, state.ShipmentID, "step 3 must not have run")
}

// TestRunSaga_IdempotentRetryReturnsCached verifies that a retry
// with the same Idempotency-Key returns the cached state and
// does NOT re-execute any step. Without this, a network-level
// retry would double-apply.
func TestRunSaga_IdempotentRetryReturnsCached(t *testing.T) {
	var reserveCalls atomic.Int32
	tracking := func(ctx context.Context, s *OrderState) error {
		reserveCalls.Add(1)
		return realInventoryReserve(ctx, s)
	}
	c := newCoordinator(tracking, realCardCharge, realShipmentDispatch)
	req := OrderRequest{OrderID: "ord-2", Amount: 10}

	first, err := c.runSaga(context.Background(), "retry-token", req)
	require.NoError(t, err)
	require.Equal(t, int32(1), reserveCalls.Load())

	second, err := c.runSaga(context.Background(), "retry-token", req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), reserveCalls.Load(), "retry must not re-execute the saga")

	// Cached state is a fresh value but content-identical.
	assert.Equal(t, first.ChargeID, second.ChargeID)
	assert.Equal(t, first.StepsApplied, second.StepsApplied)
}

// TestRunSaga_FailureNotCached pins the "do not cache failures"
// contract: a saga that failed-and-compensated does NOT poison
// the idempotency cache. The next retry with the same key
// re-runs the saga (giving the upstream sender a chance to
// have fixed whatever caused the failure).
func TestRunSaga_FailureNotCached(t *testing.T) {
	var attempts atomic.Int32
	failingThenSucceeding := func(ctx context.Context, s *OrderState) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient downstream blip")
		}
		return realShipmentDispatch(ctx, s)
	}
	c := newCoordinator(realInventoryReserve, realCardCharge, failingThenSucceeding)

	_, err := c.runSaga(context.Background(), "retry-after-fail", OrderRequest{OrderID: "ord-3", Amount: 10})
	require.Error(t, err)

	state, err := c.runSaga(context.Background(), "retry-after-fail", OrderRequest{OrderID: "ord-3", Amount: 10})
	require.NoError(t, err, "second attempt with fixed downstream must succeed")
	assert.Equal(t, "shp_ord-3", state.ShipmentID)
}

// TestRunSaga_ConcurrentRetriesSerialize verifies the per-key
// exclusive section: two concurrent callers with the same
// idempotency key serialize cleanly. The second caller's
// callable sees the cache and returns without re-executing.
func TestRunSaga_ConcurrentRetriesSerialize(t *testing.T) {
	var reserveCalls atomic.Int32
	tracking := func(ctx context.Context, s *OrderState) error {
		reserveCalls.Add(1)
		return realInventoryReserve(ctx, s)
	}
	c := newCoordinator(tracking, realCardCharge, realShipmentDispatch)

	const N = 8
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.runSaga(context.Background(), "concurrent-key", OrderRequest{OrderID: "ord-4", Amount: 10})
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), reserveCalls.Load(), "the exclusive section + cache must guarantee single execution under concurrent retries")
}

// HTTP-level smoke covering the wire shape.

func TestHandleOrder_HappyPath(t *testing.T) {
	c := newCoordinator(realInventoryReserve, realCardCharge, realShipmentDispatch)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", c.handleOrder)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(OrderRequest{OrderID: "ord-http-1", Amount: 99.99, Currency: "USD"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/orders", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "http-1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandleOrder_MissingIdempotencyKeyRejected(t *testing.T) {
	c := newCoordinator(realInventoryReserve, realCardCharge, realShipmentDispatch)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", c.handleOrder)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(OrderRequest{OrderID: "ord-http-2", Amount: 99.99})
	resp, err := srv.Client().Post(srv.URL+"/orders", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestHandleOrder_SagaFailureReturns422 pins the error-routing
// contract: compensation completed cleanly → 422 (client should
// not retry without a fix). Operators distinguish this from a
// 500 (compensation partially failed → manual intervention).
func TestHandleOrder_SagaFailureReturns422(t *testing.T) {
	failing := &failOnce{}
	c := newCoordinator(realInventoryReserve, realCardCharge, failing.fail)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", c.handleOrder)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(OrderRequest{OrderID: "ord-fail-http", Amount: 10})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/orders", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "fail-http")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}
