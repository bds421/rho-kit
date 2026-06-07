package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	kid "github.com/bds421/rho-kit/core/v2/id"
)

// DurableStep is the persistence-aware analogue of [Step]. Forward and
// Compensate take JSON byte slices so input and per-step output
// survive a process crash and resume on a different replica.
type DurableStep struct {
	// Name is a short identifier used in logs / persisted state.
	// Required and unique within a [DurableDefinition].
	Name string

	// Forward runs the step. input is the saga's initial input on the
	// first step, or the output of step i-1 on subsequent steps —
	// callers wanting cumulative state should encode it in the output.
	// The returned output is persisted to StepResults[i] so Compensate
	// can read it during rollback.
	Forward func(ctx context.Context, input []byte) (output []byte, err error)

	// Compensate undoes the step. May be nil. Receives both the input
	// the forward saw and the output it produced so the rollback can
	// distinguish what to undo.
	Compensate func(ctx context.Context, input []byte, output []byte) error
}

// DurableDefinition is the persistence-aware analogue of [Definition].
type DurableDefinition struct {
	// Name must be unique within an executor's registered set; the
	// persisted Instance.Definition stores this value so Resume can
	// look the definition up after a process restart.
	Name  string
	Steps []DurableStep
}

// Validate checks the definition for missing names / forwards. Called
// automatically by [DurableExecutor.Register].
func (d *DurableDefinition) Validate() error {
	if d == nil {
		return errors.New("saga: nil DurableDefinition")
	}
	if d.Name == "" {
		return errors.New("saga: DurableDefinition.Name is required")
	}
	if len(d.Steps) == 0 {
		return errors.New("saga: DurableDefinition requires at least one step")
	}
	seen := make(map[string]struct{}, len(d.Steps))
	for i, s := range d.Steps {
		if s.Name == "" {
			return fmt.Errorf("saga: step %d missing Name", i)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("saga: duplicate step name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
		if s.Forward == nil {
			return fmt.Errorf("saga: step %q missing Forward", s.Name)
		}
	}
	return nil
}

// DurableExecutor runs DurableDefinitions against a [StateStore],
// checkpointing state at every step boundary so a process crash can
// be recovered by [DurableExecutor.Resume] on the next start.
//
// Multiple replicas may share the same [StateStore] safely; the
// executor uses optimistic-concurrency semantics on the store (last
// writer wins for the same instance state). Use a [StateStore]
// backend that serialises updates per instance (e.g. data/saga/pgstore
// uses an UPDATE ... WHERE updated_at = $old check) to prevent two
// replicas executing the same step in parallel.
type DurableExecutor struct {
	store       StateStore
	logger      *slog.Logger
	definitions map[string]*DurableDefinition

	mu sync.Mutex
}

// ExecutorOption configures a [DurableExecutor].
type ExecutorOption func(*DurableExecutor)

// WithExecutorLogger overrides the logger. Defaults to slog.Default().
func WithExecutorLogger(l *slog.Logger) ExecutorOption {
	return func(e *DurableExecutor) { e.logger = l }
}

// NewDurableExecutor returns an executor backed by store. Register
// each [DurableDefinition] via [DurableExecutor.Register] before
// calling Start / Resume.
func NewDurableExecutor(store StateStore, opts ...ExecutorOption) (*DurableExecutor, error) {
	if store == nil {
		return nil, errors.New("saga: NewDurableExecutor requires non-nil StateStore")
	}
	e := &DurableExecutor{
		store:       store,
		definitions: make(map[string]*DurableDefinition),
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("saga: ExecutorOption must not be nil")
		}
		opt(e)
	}
	if e.logger == nil {
		e.logger = slog.Default()
	}
	return e, nil
}

// Register adds a definition to the executor. Returns an error if the
// definition is invalid or its Name is already registered.
func (e *DurableExecutor) Register(def *DurableDefinition) error {
	if err := def.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.definitions[def.Name]; dup {
		return fmt.Errorf("saga: definition %q already registered", def.Name)
	}
	e.definitions[def.Name] = def
	return nil
}

// Start enqueues a new instance with the supplied input and runs it
// to completion (or compensation). The instance ID is returned even
// on error so callers can look up the persisted state.
//
// Input is JSON-serialised; callers may pass any value json.Marshal
// accepts. Pass nil for a saga that takes no input.
func (e *DurableExecutor) Start(ctx context.Context, definitionName string, input any) (string, error) {
	def, err := e.lookupDef(definitionName)
	if err != nil {
		return "", err
	}
	var raw json.RawMessage
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return "", fmt.Errorf("saga: marshal input: %w", err)
		}
	}
	instID := kid.New()
	inst := Instance{
		ID:         instID,
		Definition: definitionName,
		State:      StatePending,
		Input:      raw,
	}
	if err := e.store.Put(ctx, inst); err != nil {
		return instID, fmt.Errorf("saga: persist new instance: %w", err)
	}
	return instID, e.executeInstance(ctx, def, instID)
}

// Run executes (or resumes) the instance with the given ID using its
// persisted state. Returns nil on successful completion, or the
// terminal error (forward + optional compensate failures).
func (e *DurableExecutor) Run(ctx context.Context, instanceID string) error {
	inst, err := e.store.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	def, err := e.lookupDef(inst.Definition)
	if err != nil {
		return err
	}
	return e.executeInstance(ctx, def, instanceID)
}

// resumeConcurrency bounds how many in-flight sagas Resume drives at once. A
// single saga can block for a long time (e.g. a step that polls an external
// resource until it is ready), so resuming sequentially lets one slow or stuck
// saga starve every other recoverable saga behind it — and, when Resume is
// called synchronously at startup, stall the whole process. Driving them
// concurrently isolates a stuck saga to its own slot; the bound keeps a large
// recovery backlog from exhausting goroutines or hammering downstream APIs.
const resumeConcurrency = 8

// Resume scans the store for non-terminal instances older than olderThan and
// runs each to completion / compensation. Use this on service startup to
// recover sagas left in flight by a previous process. Instances are driven
// CONCURRENTLY (bounded by [resumeConcurrency]) so one stuck saga cannot block
// the others. Returns the per-instance error via the summary slice (in input
// order); individual failures do not short-circuit the batch.
func (e *DurableExecutor) Resume(ctx context.Context, olderThan time.Duration) ([]ResumeResult, error) {
	pending, err := e.store.ListResumable(ctx, olderThan)
	if err != nil {
		return nil, fmt.Errorf("saga: list resumable: %w", err)
	}
	// Each goroutine writes its own distinct results[i], so no lock is needed
	// for the slice; lookupDef takes e.mu briefly but executeInstance does not,
	// and it only ever touches its own instance row in the store.
	results := make([]ResumeResult, len(pending))
	sem := make(chan struct{}, resumeConcurrency)
	var wg sync.WaitGroup
	for i, inst := range pending {
		results[i].InstanceID = inst.ID
		def, err := e.lookupDef(inst.Definition)
		if err != nil {
			results[i].Err = err
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, def *DurableDefinition, instanceID string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i].Err = e.executeInstance(ctx, def, instanceID)
		}(i, def, inst.ID)
	}
	wg.Wait()
	return results, nil
}

// ResumeResult pairs an instance ID with its resumed-execution error
// (nil on success).
type ResumeResult struct {
	InstanceID string
	Err        error
}

func (e *DurableExecutor) lookupDef(name string) (*DurableDefinition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	def, ok := e.definitions[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrDefinitionNotFound, name)
	}
	return def, nil
}

// executeInstance is the core state-machine driver. Called from Start,
// Run, and Resume — they differ only in how the instance ID arrives.
func (e *DurableExecutor) executeInstance(ctx context.Context, def *DurableDefinition, instanceID string) error {
	inst, err := e.store.Get(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.IsTerminal() {
		return nil
	}
	if inst.State == StateCompensating {
		return e.driveCompensation(ctx, def, inst)
	}

	// StatePending or StateRunning: run forward steps from CurrentStep.
	inst.State = StateRunning
	if err := e.store.Put(ctx, inst); err != nil {
		return fmt.Errorf("saga: persist running state: %w", err)
	}

	for inst.CurrentStep < len(def.Steps) {
		if err := ctx.Err(); err != nil {
			inst.LastError = err.Error()
			_ = e.store.Put(ctx, inst)
			return err
		}
		step := def.Steps[inst.CurrentStep]
		input := e.stepInput(inst)
		output, fwdErr := step.Forward(ctx, input)
		if fwdErr != nil {
			e.logger.Warn("saga forward step failed",
				slog.String("instance", inst.ID),
				slog.String("definition", def.Name),
				slog.String("step", step.Name),
				slog.String("error", fwdErr.Error()),
			)
			inst.LastError = fwdErr.Error()
			inst.State = StateCompensating
			if err := e.store.Put(ctx, inst); err != nil {
				return fmt.Errorf("saga: persist failure state: %w", err)
			}
			fwd := &ForwardError{Index: inst.CurrentStep, Name: step.Name, Cause: fwdErr}
			return e.driveCompensationAfterForward(ctx, def, inst, fwd)
		}
		// Persist the step's output before advancing CurrentStep so
		// a crash after this point still has the output for Compensate.
		// Grow StepResults to len(def.Steps) when starting fresh OR
		// when resuming a pre-crash instance whose persisted slice is
		// shorter than the definition (the crash happened before
		// later step slots were written).
		if len(inst.StepResults) < len(def.Steps) {
			grown := make([]json.RawMessage, len(def.Steps))
			copy(grown, inst.StepResults)
			inst.StepResults = grown
		}
		inst.StepResults[inst.CurrentStep] = output
		inst.CurrentStep++
		inst.LastError = ""
		if err := e.store.Put(ctx, inst); err != nil {
			return fmt.Errorf("saga: persist step advance: %w", err)
		}
	}

	inst.State = StateCompleted
	if err := e.store.Put(ctx, inst); err != nil {
		return fmt.Errorf("saga: persist completion: %w", err)
	}
	e.logger.Info("saga completed",
		slog.String("instance", inst.ID),
		slog.String("definition", def.Name),
	)
	return nil
}

// driveCompensationAfterForward is invoked when a forward step has
// just failed in the current process. It runs Compensate on steps
// [0..CurrentStep-1] in reverse order and returns the joined
// ForwardError + CompensateError, matching the in-memory [Run]
// behaviour.
func (e *DurableExecutor) driveCompensationAfterForward(ctx context.Context, def *DurableDefinition, inst Instance, fwd *ForwardError) error {
	compErr := e.driveCompensation(ctx, def, inst)
	if compErr == nil {
		return fwd
	}
	return errors.Join(fwd, compErr)
}

// driveCompensation runs compensation from the current state. May be
// called fresh (after a forward failure in this process) or as
// resume (after a crash that left an instance in StateCompensating).
//
// Walks indices from inst.CurrentStep-1 down to 0, skipping any in
// inst.Compensated. Each successful compensate is appended to
// Compensated and persisted so a re-resume doesn't double-fire.
func (e *DurableExecutor) driveCompensation(ctx context.Context, def *DurableDefinition, inst Instance) error {
	already := make(map[int]struct{}, len(inst.Compensated))
	for _, idx := range inst.Compensated {
		already[idx] = struct{}{}
	}
	var compErrs []CompensateStepError
	for i := inst.CurrentStep - 1; i >= 0; i-- {
		if _, done := already[i]; done {
			continue
		}
		step := def.Steps[i]
		if step.Compensate != nil {
			input := compensateInput(inst, i)
			var output []byte
			if i < len(inst.StepResults) {
				output = inst.StepResults[i]
			}
			if err := step.Compensate(ctx, input, output); err != nil {
				e.logger.Warn("saga compensate step failed",
					slog.String("instance", inst.ID),
					slog.String("step", step.Name),
					slog.String("error", err.Error()),
				)
				compErrs = append(compErrs, CompensateStepError{Index: i, Name: step.Name, Cause: err})
			}
		}
		inst.Compensated = append(inst.Compensated, i)
		if err := e.store.Put(ctx, inst); err != nil {
			return fmt.Errorf("saga: persist compensated %d: %w", i, err)
		}
	}
	inst.State = StateFailed
	if err := e.store.Put(ctx, inst); err != nil {
		return fmt.Errorf("saga: persist failed state: %w", err)
	}
	if len(compErrs) == 0 {
		return nil
	}
	return &CompensateError{Errors: compErrs}
}

// stepInput returns the bytes passed to step CurrentStep's Forward.
// For step 0 it's the saga's initial input; for step N>0 it's the
// previous step's output (StepResults[N-1]).
func (e *DurableExecutor) stepInput(inst Instance) []byte {
	if inst.CurrentStep == 0 {
		return inst.Input
	}
	if inst.CurrentStep-1 < len(inst.StepResults) {
		return inst.StepResults[inst.CurrentStep-1]
	}
	return nil
}

// compensateInput returns the bytes passed as `input` to step idx's
// Compensate: the saga's initial input for step 0, otherwise
// StepResults[idx-1].
func compensateInput(inst Instance, idx int) []byte {
	if idx == 0 {
		return inst.Input
	}
	if idx-1 < len(inst.StepResults) {
		return inst.StepResults[idx-1]
	}
	return nil
}
