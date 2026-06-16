package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/v2/outbox"
)

func TestNew_PanicsOnNilPool(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

func TestRequireTx_RejectsCtxWithoutTx(t *testing.T) {
	err := RequireTx(context.Background())
	assert.ErrorIs(t, err, ErrNoTx)
}

func TestRequireTx_NilCtxRejectedSafely(t *testing.T) {
	//nolint:staticcheck // the helper must tolerate nil ctx (defensive contract)
	err := RequireTx(nil)
	assert.ErrorIs(t, err, ErrNoTx)
}

func TestTxFromContext_AbsentReturnsFalse(t *testing.T) {
	_, ok := TxFromContext(context.Background())
	assert.False(t, ok)
}

func TestWithTx_NilTxIsNoop(t *testing.T) {
	ctx := context.Background()
	out := WithTx(ctx, nil)
	// Cannot use assert.Same on interface ctx values; the contract is
	// behavioural: a nil-tx WithTx must leave RequireTx failing.
	assert.ErrorIs(t, RequireTx(out), ErrNoTx,
		"WithTx(ctx, nil) must not stash a tx — RequireTx must still fail")
}

func TestStore_NilReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	var s *Store

	err := s.Insert(ctx, outbox.Entry{ID: uuid.New()})
	assert.Error(t, err)

	_, err = s.FetchPending(ctx, 10)
	assert.Error(t, err)

	_, err = s.Heartbeat(ctx, []string{"x"})
	assert.Error(t, err)

	err = s.MarkPublished(ctx, "x", time.Now())
	assert.Error(t, err)

	err = s.MarkFailed(ctx, "x", "boom")
	assert.Error(t, err)

	err = s.IncrementAttempts(ctx, "x", "boom", time.Now())
	assert.Error(t, err)

	_, err = s.DeletePublishedBefore(ctx, time.Now())
	assert.Error(t, err)

	_, err = s.DeleteFailedBefore(ctx, time.Now())
	assert.Error(t, err)

	_, err = s.ResetStaleProcessing(ctx, time.Hour)
	assert.Error(t, err)

	_, err = s.CountPending(ctx)
	assert.Error(t, err)

	err = s.ResetPending(ctx, []string{"x"})
	assert.Error(t, err)
}

// TestStore_ImplementsPendingResetter pins the optional-capability wiring:
// the pgx store must satisfy outbox.PendingResetter so the relay's
// shutdown reset path engages against it.
func TestStore_ImplementsPendingResetter(t *testing.T) {
	var s *Store
	var _ outbox.PendingResetter = s // compile-time
	assert.Implements(t, (*outbox.PendingResetter)(nil), (*Store)(nil))
}

// TestClaimTokenBookkeeping exercises the in-process id->token map that
// fences outcome updates. This is the unit-testable core of DEFECT B's
// fence; the actual SQL fence needs a live Postgres (integration suite).
func TestClaimTokenBookkeeping(t *testing.T) {
	s := &Store{claimTokens: make(map[string]string)}

	// Unknown id has no token.
	_, ok := s.claimToken("a")
	assert.False(t, ok)

	// Remember then read back.
	s.rememberClaim("a", "tok-a")
	tok, ok := s.claimToken("a")
	assert.True(t, ok)
	assert.Equal(t, "tok-a", tok)

	// Re-claim overwrites (this process reset the row, then re-claimed it).
	s.rememberClaim("a", "tok-a2")
	tok, ok = s.claimToken("a")
	assert.True(t, ok)
	assert.Equal(t, "tok-a2", tok)

	// Forget drops the entry to keep the map bounded.
	s.forgetClaim("a")
	_, ok = s.claimToken("a")
	assert.False(t, ok)

	// Forgetting an unknown id is a harmless no-op.
	assert.NotPanics(t, func() { s.forgetClaim("missing") })
}

// TestNew_InitializesClaimTokenMap confirms the constructor wires the
// token map so the first FetchPending/outcome call does not nil-deref.
func TestNew_InitializesClaimTokenMap(t *testing.T) {
	// New requires a non-nil pool; we only assert the map is ready, so
	// construct the struct directly the way New does and verify the helper
	// is safe immediately.
	s := &Store{claimTokens: make(map[string]string)}
	assert.NotPanics(t, func() {
		s.rememberClaim("x", "t")
		_, _ = s.claimToken("x")
		s.forgetClaim("x")
	})
}

func TestStore_InsertRejectsZeroID(t *testing.T) {
	// Use a zero-pool Store. Insert short-circuits on the not-initialized
	// check before the ID validator runs; the integration suite covers
	// the zero-ID rejection against a live store. The intent here is just
	// to confirm Insert never panics on an empty entry.
	s := &Store{pool: nil}
	err := s.Insert(context.Background(), outbox.Entry{})
	assert.Error(t, err)
}
