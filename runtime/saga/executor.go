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
	"github.com/bds421/rho-kit/core/v2/redact"
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
// Multiple replicas may share the same [StateStore] safely only when
// the backend serialises ownership per instance (e.g. data/saga/pgstore
// claim leases). Within a single process the executor holds a per-instance
// mutex so concurrent Start/Run/Resume of the same ID cannot double-fire
// Forward steps. Cross-process safety still depends on the store.
type DurableExecutor struct {
	store       StateStore
	logger      *slog.Logger
	definitions map[string]*DurableDefinition

	mu sync.Mutex
	// instanceMu serialises executeInstance per saga ID inside this
	// process. Without it two goroutines can both Get the same
	// non-terminal instance, both observe CurrentStep=N, and both
	// invoke Steps[N].Forward — double-applying external side effects.
	instanceMu sync.Map // map[string]*sync.Mutex
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
// concurrently lets up to resumeConcurrency slow sagas make progress in
// parallel; the bound keeps a large recovery backlog from exhausting goroutines
// or hammering downstream APIs.
//
// The bound is a throughput limit, not a hard isolation guarantee: it caps the
// blast radius to resumeConcurrency slots, but it does not cap any individual
// saga's runtime. If resumeConcurrency or more sagas each block forever in a
// step that ignores ctx, every slot fills, the dispatch loop blocks acquiring
// the next slot, and Resume itself stalls until one of them returns. Steps that
// poll must honour ctx cancellation (and callers should pass a cancellable ctx)
// so a stuck saga can be unblocked rather than pinning a slot indefinitely.
const resumeConcurrency = 8

// Resume scans the store for non-terminal instances older than olderThan and
// runs each to completion / compensation. Use this on service startup to
// recover sagas left in flight by a previous process. Instances are driven
// CONCURRENTLY (up to [resumeConcurrency] at a time) so a small number of slow
// or stuck sagas do not block the others. Returns the per-instance error via
// the summary slice (in input order); individual failures (including a
// panicking step) do not short-circuit the batch.
//
// The concurrency bound is not a per-saga timeout: if [resumeConcurrency] or
// more recovered sagas each block forever in a ctx-ignoring step, all slots
// fill and Resume itself stalls. Pass a cancellable ctx and write steps that
// honour cancellation so a stuck saga can be released.
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
			// A panicking user step (Forward/Compensate) must not crash
			// the host process or short-circuit the batch — it surfaces
			// as this instance's ResumeResult.Err while siblings keep
			// running. The raw panic value is redacted because it may
			// carry tokens, request bodies, or domain structs.
			defer func() {
				if rec := recover(); rec != nil {
					e.logger.Error("saga resume step panicked",
						slog.String("instance", instanceID),
						slog.String("definition", def.Name),
						redact.Panic(rec),
					)
					results[i].Err = fmt.Errorf("saga: resume panicked: %s", redact.PanicValue(rec))
				}
			}()
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
//
// Panic recovery lives here (not only in Resume) so a panicking Forward
// / Compensate during Start/Run yields an error instead of crashing the
// caller and leaving the instance stuck in StateRunning with no
// LastError and no compensation attempted.
func (e *DurableExecutor) executeInstance(ctx context.Context, def *DurableDefinition, instanceID string) (err error) {
	unlock := e.lockInstance(instanceID)
	defer unlock()

	defer func() {
		if rec := recover(); rec != nil {
			e.logger.Error("saga step panicked",
				slog.String("instance", instanceID),
				slog.String("definition", def.Name),
				redact.Panic(rec),
			)
			// Best-effort: persist a failure marker so Resume can pick up.
			if inst, getErr := e.store.Get(ctx, instanceID); getErr == nil && !inst.IsTerminal() {
				inst.LastError = redact.PanicValue(rec)
				inst.State = StateCompensating
				_ = e.store.Put(ctx, inst)
			}
			err = fmt.Errorf("saga: step panicked: %s", redact.PanicValue(rec))
		}
	}()

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
			inst.LastError = redact.ErrorValue(err)
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
				redact.Error(fwdErr),
			)
			inst.LastError = redact.ErrorValue(fwdErr)
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
// inst.Compensated. Each compensate attempt (success OR failure) is
// appended to Compensated and persisted — compensation is best-effort
// and is not re-attempted on Resume for a previously-tried index.
// Callers needing retry-on-compensate-failure should keep the step
// out of Compensated themselves via a custom StateStore.
//
// Compensation runs under context.WithoutCancel so a cancelled or
// deadline-expired caller (typical for HTTP-driven Start) cannot
// abort rollback mid-way, matching the in-memory [Run] rollBack path.
func (e *DurableExecutor) driveCompensation(ctx context.Context, def *DurableDefinition, inst Instance) error {
	// Detach cancellation/deadline; preserve values. Match saga.Run's
	// rollBack so a timed-out request still completes compensation.
	compCtx := context.WithoutCancel(ctx)
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
			if err := step.Compensate(compCtx, input, output); err != nil {
				e.logger.Warn("saga compensate step failed",
					slog.String("instance", inst.ID),
					slog.String("step", step.Name),
					redact.Error(err),
				)
				compErrs = append(compErrs, CompensateStepError{Index: i, Name: step.Name, Cause: err})
			}
		}
		inst.Compensated = append(inst.Compensated, i)
		if err := e.store.Put(compCtx, inst); err != nil {
			return fmt.Errorf("saga: persist compensated %d: %w", i, err)
		}
	}
	inst.State = StateFailed
	if err := e.store.Put(compCtx, inst); err != nil {
		return fmt.Errorf("saga: persist failed state: %w", err)
	}
	if len(compErrs) == 0 {
		return nil
	}
	return &CompensateError{Errors: compErrs}
}

// lockInstance acquires the per-instance mutex for id and returns an
// unlock func. Safe for concurrent use; the mutex is retained in the
// map for the process lifetime (saga IDs are not unbounded hot keys —
// each Start mints a new ID and completed instances are not re-driven).
func (e *DurableExecutor) lockInstance(id string) (unlock func()) {
	v, _ := e.instanceMu.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
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
