package saga_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/bds421/rho-kit/runtime/v2/saga"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recorder struct {
	forward     []string
	compensated []string
}

func (r *recorder) step(name string, forwardErr, compensateErr error) saga.Step {
	return saga.Step{
		Name: name,
		Forward: func(_ context.Context, _ any) error {
			r.forward = append(r.forward, name)
			return forwardErr
		},
		Compensate: func(_ context.Context, _ any) error {
			r.compensated = append(r.compensated, name)
			return compensateErr
		},
	}
}

func TestNewDefinition_RejectsEmpty(t *testing.T) {
	def, err := saga.NewDefinition()
	assert.Nil(t, def)
	assert.Error(t, err)
}

func TestNewDefinition_RequiresName(t *testing.T) {
	_, err := saga.NewDefinition(saga.Step{Forward: func(context.Context, any) error { return nil }})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing Name")
}

func TestNewDefinition_RequiresForward(t *testing.T) {
	_, err := saga.NewDefinition(saga.Step{Name: "noop"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing Forward")
}

func TestMustDefinition_PanicsOnError(t *testing.T) {
	assert.Panics(t, func() { saga.MustDefinition() })
}

func TestRun_AllStepsSucceed(t *testing.T) {
	r := &recorder{}
	def := saga.MustDefinition(
		r.step("a", nil, nil),
		r.step("b", nil, nil),
		r.step("c", nil, nil),
	)

	require.NoError(t, saga.Run(context.Background(), def, nil))
	assert.Equal(t, []string{"a", "b", "c"}, r.forward)
	assert.Empty(t, r.compensated, "no compensations run on success")
}

func TestRun_RollsBackInReverseOnFailure(t *testing.T) {
	r := &recorder{}
	boom := errors.New("step c boom")
	def := saga.MustDefinition(
		r.step("a", nil, nil),
		r.step("b", nil, nil),
		saga.Step{
			Name:    "c",
			Forward: func(_ context.Context, _ any) error { r.forward = append(r.forward, "c"); return boom },
		},
		r.step("d", nil, nil),
	)

	err := saga.Run(context.Background(), def, nil)
	require.Error(t, err)

	var fe *saga.ForwardError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, 2, fe.Index)
	assert.Equal(t, "c", fe.Name)
	assert.ErrorIs(t, err, boom)

	assert.Equal(t, []string{"a", "b", "c"}, r.forward, "d must NOT run after c fails")
	assert.Equal(t, []string{"b", "a"}, r.compensated, "compensations run in reverse for prior steps only")
}

func TestRun_NilCompensateIsSkipped(t *testing.T) {
	r := &recorder{}
	def := saga.MustDefinition(
		saga.Step{
			Name:    "non-compensable",
			Forward: func(_ context.Context, _ any) error { r.forward = append(r.forward, "non-compensable"); return nil },
		},
		r.step("b", errors.New("fail"), nil),
	)

	err := saga.Run(context.Background(), def, nil)
	require.Error(t, err)
	assert.Empty(t, r.compensated, "step with nil Compensate must not abort rollback")
}

func TestRun_BestEffortRollbackCollectsCompensateErrors(t *testing.T) {
	r := &recorder{}
	compErrA := errors.New("compensate a failed")
	compErrB := errors.New("compensate b failed")
	forwardErr := errors.New("step c forward failed")

	def := saga.MustDefinition(
		r.step("a", nil, compErrA),
		r.step("b", nil, compErrB),
		saga.Step{Name: "c", Forward: func(context.Context, any) error { return forwardErr }},
	)

	err := saga.Run(context.Background(), def, nil)
	require.Error(t, err)

	var fe *saga.ForwardError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, "c", fe.Name)

	var ce *saga.CompensateError
	require.ErrorAs(t, err, &ce)
	require.Len(t, ce.Errors, 2)
	assert.Equal(t, "b", ce.Errors[0].Name, "compensation failures appear in roll-back order (reverse)")
	assert.Equal(t, "a", ce.Errors[1].Name)
	assert.ErrorIs(t, err, compErrA)
	assert.ErrorIs(t, err, compErrB)
	assert.ErrorIs(t, err, forwardErr)

	assert.Equal(t, []string{"b", "a"}, r.compensated, "rollback continues past a failed compensation")
}

func TestRun_StatePassedThroughEveryStep(t *testing.T) {
	type acc struct{ seen []string }
	state := &acc{}
	def := saga.MustDefinition(
		saga.Step{Name: "a", Forward: func(_ context.Context, s any) error {
			s.(*acc).seen = append(s.(*acc).seen, "a")
			return nil
		}},
		saga.Step{Name: "b", Forward: func(_ context.Context, s any) error {
			s.(*acc).seen = append(s.(*acc).seen, "b")
			return nil
		}},
	)
	require.NoError(t, saga.Run(context.Background(), def, state))
	assert.Equal(t, []string{"a", "b"}, state.seen)
}

func TestRun_CancelledContextBeforeForwardTriggersRollback(t *testing.T) {
	r := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	def := saga.MustDefinition(
		r.step("a", nil, nil),
		saga.Step{
			Name: "b",
			Forward: func(_ context.Context, _ any) error {
				cancel()
				r.forward = append(r.forward, "b")
				return nil
			},
			Compensate: func(context.Context, any) error { r.compensated = append(r.compensated, "b"); return nil },
		},
		r.step("c", nil, nil),
	)

	err := saga.Run(ctx, def, nil)
	require.Error(t, err)

	var fe *saga.ForwardError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, 2, fe.Index, "ctx.Err() is checked before step c, so the saga fails at index 2")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"b", "a"}, r.compensated, "both completed forwards must be compensated")
}

// TestRun_RollbackDetachedFromCancelledContext proves the documented contract:
// when the parent ctx is cancelled (which itself triggers rollback), a
// ctx-respecting Compensate still runs to completion rather than aborting on the
// inherited cancellation. Best-effort rollback must not be defeated by the same
// cancellation that started it.
func TestRun_RollbackDetachedFromCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var compensated []string
	var compCtxErr error
	def := saga.MustDefinition(
		saga.Step{
			Name:    "a",
			Forward: func(context.Context, any) error { return nil },
			Compensate: func(cctx context.Context, _ any) error {
				// A ctx-respecting Compensate: under the bug it sees the
				// cancelled parent ctx and refuses to do its work.
				compCtxErr = cctx.Err()
				if cctx.Err() != nil {
					return cctx.Err()
				}
				compensated = append(compensated, "a")
				return nil
			},
		},
		saga.Step{
			Name: "b",
			Forward: func(context.Context, any) error {
				cancel() // cancel the parent; ctx.Err() is checked before step c
				return nil
			},
		},
		saga.Step{Name: "c", Forward: func(context.Context, any) error { return nil }},
	)

	err := saga.Run(ctx, def, nil)
	require.Error(t, err)
	require.NoError(t, compCtxErr, "Compensate must receive a non-cancelled (detached) context")
	require.Equal(t, []string{"a"}, compensated, "ctx-respecting compensation must run to completion")
}

func TestRun_NilDefinitionReturnsError(t *testing.T) {
	err := saga.Run(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil Definition")
}

func TestRun_NilContextReturnsError(t *testing.T) {
	def := saga.MustDefinition(saga.Step{Name: "a", Forward: func(context.Context, any) error { return nil }})
	//nolint:staticcheck // SA1012 — deliberately exercising the nil-ctx guard.
	//lint:ignore SA1012 nil-ctx rejection contract test
	err := saga.Run(nil, def, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestForwardError_Format(t *testing.T) {
	fe := &saga.ForwardError{Index: 3, Name: "reserve", Cause: errors.New("stock empty")}
	assert.Contains(t, fe.Error(), "step 3")
	assert.Contains(t, fe.Error(), `"reserve"`)
	assert.Contains(t, fe.Error(), "stock empty")
	assert.Equal(t, fe.Cause, errors.Unwrap(fe))
}

func TestCompensateError_FormatWithEntries(t *testing.T) {
	ce := &saga.CompensateError{Errors: []saga.CompensateStepError{
		{Index: 1, Name: "release-lock", Cause: errors.New("redis offline")},
	}}
	assert.Contains(t, ce.Error(), "step 1")
	assert.Contains(t, ce.Error(), "release-lock")
	assert.Contains(t, ce.Error(), "redis offline")
}

func TestCompensateError_FormatEmpty(t *testing.T) {
	ce := &saga.CompensateError{}
	assert.Equal(t, "saga: compensate failed", ce.Error())
}

func TestSteps_ReturnsDefensiveCopy(t *testing.T) {
	def := saga.MustDefinition(saga.Step{
		Name:    "a",
		Forward: func(context.Context, any) error { return nil },
	})
	steps := def.Steps()
	steps[0].Name = "MUTATED"

	again := def.Steps()
	assert.Equal(t, "a", again[0].Name, "external mutation must not affect the Definition")
}

func ExampleRun() {
	type orderState struct {
		debited  bool
		reserved bool
	}

	def := saga.MustDefinition(
		saga.Step{
			Name: "debit-wallet",
			Forward: func(_ context.Context, s any) error {
				s.(*orderState).debited = true
				return nil
			},
			Compensate: func(_ context.Context, s any) error {
				s.(*orderState).debited = false
				return nil
			},
		},
		saga.Step{
			Name: "reserve-inventory",
			Forward: func(_ context.Context, _ any) error {
				return errors.New("out of stock")
			},
		},
	)

	state := &orderState{}
	err := saga.Run(context.Background(), def, state)
	fmt.Println("err:", err != nil, "debited:", state.debited, "reserved:", state.reserved)
	// Output: err: true debited: false reserved: false
}
