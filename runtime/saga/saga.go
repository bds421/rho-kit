package saga

import (
	"context"
	"errors"
	"fmt"
)

// Step is one stage of a saga. Forward runs the action; Compensate
// runs the rollback for that action. Both receive the saga's
// in-flight state as `any` so call sites can pass a typed value
// across steps without the saga package needing to know the type.
//
// Compensate is invoked only for steps whose Forward already
// returned without error. A step whose Forward failed has nothing
// to compensate (the failure itself is the rollback); the executor
// rolls back the prior steps and stops.
//
// Both callbacks must respect ctx cancellation. A long-running
// Forward that ignores ctx will block the saga's roll-forward, and
// a long-running Compensate will block the roll-back; either case
// is observable to the caller of [Run] as a context error.
type Step struct {
	// Name is a short identifier used in error messages and
	// log/tracing attributes. Required.
	Name string

	// Forward is the action invoked during roll-forward. Must be
	// idempotent so a retry of the step (driven by the executor
	// or future redisqueue layer) does not double-apply.
	Forward func(ctx context.Context, state any) error

	// Compensate undoes the side effect of Forward when a later
	// step fails. May be nil for steps whose forward action has
	// no rollback (a query, a no-op log entry). Nil compensations
	// are skipped at roll-back time — the saga does NOT fail just
	// because a step is non-compensable, since the saga's overall
	// rollback is best-effort by design.
	//
	// Like Forward, must be idempotent: the executor may re-invoke
	// after a crash if the saga's state table marks compensation
	// as in-flight.
	Compensate func(ctx context.Context, state any) error
}

// Definition is an ordered list of steps. Construct via
// [NewDefinition] (validates non-empty + names). The Definition is
// immutable after construction — running the same Definition
// concurrently with different states is safe.
type Definition struct {
	steps []Step
}

// NewDefinition returns a Definition for steps. Returns an error if
// steps is empty, any step is missing a Name, or any step is missing
// a Forward action. Empty Compensate is allowed (some steps have no
// rollback semantics).
func NewDefinition(steps ...Step) (*Definition, error) {
	if len(steps) == 0 {
		return nil, errors.New("saga: NewDefinition requires at least one step")
	}
	for i, s := range steps {
		if s.Name == "" {
			return nil, fmt.Errorf("saga: step %d missing Name", i)
		}
		if s.Forward == nil {
			return nil, fmt.Errorf("saga: step %q missing Forward action", s.Name)
		}
	}
	return &Definition{steps: append([]Step(nil), steps...)}, nil
}

// MustDefinition is the panicking constructor for use in package-
// level var declarations.
func MustDefinition(steps ...Step) *Definition {
	d, err := NewDefinition(steps...)
	if err != nil {
		panic(err)
	}
	return d
}

// Steps returns the (defensively-copied) step list, primarily for
// inspection in tests.
func (d *Definition) Steps() []Step {
	return append([]Step(nil), d.steps...)
}

// ForwardError reports the index, step name, and underlying cause
// of a roll-forward failure. The executor wraps the failing step's
// Forward return value in this so callers can introspect which
// step broke without parsing strings.
type ForwardError struct {
	Index int
	Name  string
	Cause error
}

func (e *ForwardError) Error() string {
	return fmt.Sprintf("saga: step %d (%q) forward failed: %s", e.Index, e.Name, e.Cause)
}

func (e *ForwardError) Unwrap() error { return e.Cause }

// CompensateError reports failures encountered during roll-back.
// Compensations are attempted best-effort — a Compensate failure
// does NOT short-circuit the roll-back; the executor continues
// rolling back earlier steps. CompensateError.Errors holds one
// entry per failed compensation in roll-back order.
type CompensateError struct {
	Errors []CompensateStepError
}

// CompensateStepError pairs a compensation failure with its step.
// Implements error and Unwrap so callers can match a specific
// compensation failure via errors.Is/errors.As against the joined
// error returned by [Run].
type CompensateStepError struct {
	Index int
	Name  string
	Cause error
}

func (e *CompensateStepError) Error() string {
	return fmt.Sprintf("saga: step %d (%q) compensate failed: %s", e.Index, e.Name, e.Cause)
}

func (e *CompensateStepError) Unwrap() error { return e.Cause }

func (e *CompensateError) Error() string {
	if len(e.Errors) == 0 {
		return "saga: compensate failed"
	}
	return fmt.Sprintf("saga: %d compensation(s) failed; first: step %d (%q): %s",
		len(e.Errors), e.Errors[0].Index, e.Errors[0].Name, e.Errors[0].Cause)
}

// Unwrap returns the per-step compensation failures so errors.Is and
// errors.As walk into them. Returning a slice (Go 1.20 multi-error
// semantics) lets callers match any one specific cause without having
// to iterate Errors manually.
func (e *CompensateError) Unwrap() []error {
	if len(e.Errors) == 0 {
		return nil
	}
	out := make([]error, len(e.Errors))
	for i := range e.Errors {
		out[i] = &e.Errors[i]
	}
	return out
}

// Run executes def's steps in order against state. On the first
// Forward failure it rolls back by invoking Compensate on each
// previously-completed step in reverse order.
//
// Returns nil on success (every Forward returned nil).
// Returns a [*ForwardError] when a step's Forward failed and
// compensation either succeeded or was a no-op.
// Returns errors.Join of the [*ForwardError] and a
// [*CompensateError] when one or more compensations also failed —
// callers checking errors.Is for either type still match.
//
// The state argument is the same value handed to every Step.
// Steps that need typed state can do a single type assertion at
// each callback boundary; the saga package stays oblivious to the
// concrete shape.
func Run(ctx context.Context, def *Definition, state any) error {
	if def == nil {
		return errors.New("saga: Run requires a non-nil Definition")
	}
	if ctx == nil {
		return errors.New("saga: Run requires a non-nil context")
	}

	for i, step := range def.steps {
		if err := ctx.Err(); err != nil {
			return rollBack(ctx, def.steps, i, state, &ForwardError{Index: i, Name: step.Name, Cause: err})
		}
		if err := step.Forward(ctx, state); err != nil {
			return rollBack(ctx, def.steps, i, state, &ForwardError{Index: i, Name: step.Name, Cause: err})
		}
	}
	return nil
}

// rollBack runs Compensate on steps[0:failedIndex] in reverse
// order. Compensations whose Compensate field is nil are skipped.
// Compensation failures are collected; if any are present, the
// returned error joins the originating ForwardError with a
// CompensateError so callers can match either via errors.As.
func rollBack(ctx context.Context, steps []Step, failedIndex int, state any, fe *ForwardError) error {
	var compErrors []CompensateStepError
	for i := failedIndex - 1; i >= 0; i-- {
		step := steps[i]
		if step.Compensate == nil {
			continue
		}
		// Use a fresh detached context inside ctx.Done so that a
		// cancelled parent doesn't abort the rollback midway —
		// rollback is best-effort completion. Callers needing a
		// hard cap on rollback time should use context.WithTimeout
		// in the Run-call ctx.
		if err := step.Compensate(ctx, state); err != nil {
			compErrors = append(compErrors, CompensateStepError{Index: i, Name: step.Name, Cause: err})
		}
	}
	if len(compErrors) == 0 {
		return fe
	}
	return errors.Join(fe, &CompensateError{Errors: compErrors})
}
