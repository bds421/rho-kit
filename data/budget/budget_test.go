package budget_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/budget"
)

// staticBudget is a trivial in-test implementation used to verify
// the interface contract documented in package docs (zero-amount
// peek behaviour and error sentinels). It is NOT exported and is
// not meant for production use.
type staticBudget struct {
	cap, used int64
}

func (s *staticBudget) Consume(_ context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	if err := budget.ValidateKey(key); err != nil {
		return false, 0, 0, err
	}
	if amount < 0 {
		return false, 0, 0, budget.ErrInvalidAmount
	}
	if s.used+amount > s.cap {
		return false, s.cap - s.used, time.Hour, nil
	}
	s.used += amount
	return true, s.cap - s.used, 0, nil
}

func (s *staticBudget) Peek(_ context.Context, key string) (int64, error) {
	if err := budget.ValidateKey(key); err != nil {
		return 0, err
	}
	return s.cap - s.used, nil
}

func TestSentinels_Distinct(t *testing.T) {
	// Sentinels are public API and must remain distinct so callers
	// can errors.Is each branch.
	assert.NotErrorIs(t, budget.ErrInvalidKey, budget.ErrInvalidAmount)
	assert.NotErrorIs(t, budget.ErrInvalidAmount, budget.ErrInvalidKey)
	assert.NotErrorIs(t, budget.ErrInvalidBudget, budget.ErrInvalidKey)
	assert.NotErrorIs(t, budget.ErrInvalidBudget, budget.ErrInvalidAmount)
}

func TestBudget_InterfaceUsable(t *testing.T) {
	var b budget.Budget = &staticBudget{cap: 100}
	ok, rem, _, err := b.Consume(context.Background(), "k", 10)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(90), rem)
}

func TestBudget_InvalidKeyFromInterface(t *testing.T) {
	var b budget.Budget = &staticBudget{cap: 100}
	_, _, _, err := b.Consume(context.Background(), "", 1)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
	_, err = b.Peek(context.Background(), "")
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestValidateKey_PortableContract(t *testing.T) {
	valid := []string{
		"tenant:acme",
		strings.Repeat("a", budget.MaxKeyLen),
	}
	for _, key := range valid {
		t.Run("valid/"+key[:min(len(key), 16)], func(t *testing.T) {
			assert.NoError(t, budget.ValidateKey(key))
		})
	}

	invalid := []string{
		"",
		strings.Repeat("a", budget.MaxKeyLen+1),
		"tenant\x00acme",
		"tenant acme",
		"tenant\tacme",
		string([]byte{0xff, 0xfe}),
	}
	for _, key := range invalid {
		t.Run("invalid", func(t *testing.T) {
			assert.ErrorIs(t, budget.ValidateKey(key), budget.ErrInvalidKey)
		})
	}
}

// refundingBudget exposes [budget.Refunder] in addition to the base
// interface so we can assert the optional-capability dispatch.
type refundingBudget struct {
	*staticBudget
	refunded int64
}

func (r *refundingBudget) Refund(_ context.Context, _ string, amount int64) (int64, error) {
	r.refunded += amount
	r.used -= amount
	if r.used < 0 {
		r.used = 0
	}
	return r.cap - r.used, nil
}

func TestRefund_DispatchesToRefunderWhenAvailable(t *testing.T) {
	rb := &refundingBudget{staticBudget: &staticBudget{cap: 100, used: 30}}
	rem, ok, err := budget.Refund(context.Background(), rb, "k", 10)
	require.NoError(t, err)
	assert.True(t, ok, "Refund must report ok=true when backend implements Refunder")
	assert.Equal(t, int64(80), rem)
	assert.Equal(t, int64(10), rb.refunded)
}

func TestRefund_FallsBackWhenBackendCannotRefund(t *testing.T) {
	plain := &staticBudget{cap: 100}
	_, ok, err := budget.Refund(context.Background(), plain, "k", 5)
	require.NoError(t, err)
	assert.False(t, ok, "non-Refunder backend must report ok=false, no error")
}

func TestRefund_RejectsNilBudget(t *testing.T) {
	var nilBudget budget.Budget
	_, ok, err := budget.Refund(context.Background(), nilBudget, "k", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidBudget)
	assert.False(t, ok)

	var typedNil *refundingBudget
	_, ok, err = budget.Refund(context.Background(), typedNil, "k", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidBudget)
	assert.False(t, ok)
}

// Validation runs at the helper level so callers see consistent
// errors regardless of optional backend capability — a bad refund
// must not look like a harmless unsupported refund.
func TestRefund_ValidatesArgumentsBeforeBackendDispatch(t *testing.T) {
	plain := &staticBudget{cap: 100}

	_, ok, err := budget.Refund(context.Background(), plain, "", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
	assert.False(t, ok)

	_, ok, err = budget.Refund(context.Background(), plain, strings.Repeat("a", budget.MaxKeyLen+1), 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
	assert.False(t, ok)

	_, ok, err = budget.Refund(context.Background(), plain, "k", -1)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
	assert.False(t, ok)

	rb := &refundingBudget{staticBudget: &staticBudget{cap: 100, used: 30}}
	_, ok, err = budget.Refund(context.Background(), rb, "k", -1)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
	assert.False(t, ok)
	assert.Equal(t, int64(0), rb.refunded, "Refunder must not be invoked for invalid amounts")
}
