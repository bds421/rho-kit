package circuitbreaker

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_OpenAfterThresholdFailures(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	_ = cb.Execute(func() error { return errors.New("fail 1") })
	assert.Equal(t, "closed", cb.State())

	_ = cb.Execute(func() error { return errors.New("fail 2") })
	assert.Equal(t, "open", cb.State())

	err := cb.Execute(func() error { return nil })
	assert.ErrorIs(t, err, ErrCircuitOpen)
}

func TestCircuitBreaker_HalfOpenClosesOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	_ = cb.Execute(func() error { return errors.New("fail") })
	assert.Equal(t, "open", cb.State())

	time.Sleep(15 * time.Millisecond)

	err := cb.Execute(func() error { return nil })
	assert.NoError(t, err)
	assert.Equal(t, "closed", cb.State())
}

func TestCircuitBreaker_HalfOpenReopensOnFailure(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	_ = cb.Execute(func() error { return errors.New("fail") })
	assert.Equal(t, "open", cb.State())

	time.Sleep(15 * time.Millisecond)

	err := cb.Execute(func() error { return errors.New("fail again") })
	assert.Error(t, err)
	assert.Equal(t, "open", cb.State())
}

func TestCircuitBreaker_IsSuccessfulOption(t *testing.T) {
	sentinel := errors.New("permanent")
	cb := NewCircuitBreaker(1, time.Minute, WithIsSuccessful(func(err error) bool {
		return err == nil || errors.Is(err, sentinel)
	}))

	err := cb.Execute(func() error { return sentinel })
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, "closed", cb.State())
}

func TestCircuitBreaker_OnStateChange(t *testing.T) {
	var transitions []State
	cb := NewCircuitBreaker(1, 10*time.Millisecond, WithOnStateChange(func(_ string, _ State, to State) {
		transitions = append(transitions, to)
	}))

	_ = cb.Execute(func() error { return errors.New("fail") })
	if len(transitions) == 0 || transitions[0] != StateOpen {
		t.Fatalf("expected first transition to open, got %v", transitions)
	}

	time.Sleep(20 * time.Millisecond)

	err := cb.Execute(func() error { return nil })
	assert.NoError(t, err)

	hasHalfOpen := false
	for _, state := range transitions {
		if state == StateHalfOpen {
			hasHalfOpen = true
		}
	}
	if !hasHalfOpen {
		t.Fatalf("expected half-open transition, got %v", transitions)
	}
	if transitions[len(transitions)-1] != StateClosed {
		t.Fatalf("expected last transition to closed, got %v", transitions)
	}
}
