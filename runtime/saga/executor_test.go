package saga_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/runtime/v2/saga"
)

func passingStep(name string, recorder *callRecorder) saga.DurableStep {
	return saga.DurableStep{
		Name: name,
		Forward: func(_ context.Context, in []byte) ([]byte, error) {
			recorder.record("fwd:" + name)
			return append([]byte(nil), in...), nil
		},
		Compensate: func(_ context.Context, _ []byte, _ []byte) error {
			recorder.record("comp:" + name)
			return nil
		},
	}
}

func failingForward(name string, recorder *callRecorder, err error) saga.DurableStep {
	return saga.DurableStep{
		Name: name,
		Forward: func(_ context.Context, _ []byte) ([]byte, error) {
			recorder.record("fwd:" + name)
			return nil, err
		},
		Compensate: func(_ context.Context, _ []byte, _ []byte) error {
			recorder.record("comp:" + name)
			return nil
		},
	}
}

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *callRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func TestDurableExecutor_HappyPath(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, err := saga.NewDurableExecutor(store)
	require.NoError(t, err)
	rec := &callRecorder{}
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "test-saga",
		Steps: []saga.DurableStep{
			passingStep("a", rec),
			passingStep("b", rec),
			passingStep("c", rec),
		},
	}))

	instID, err := exec.Start(context.Background(), "test-saga", nil)
	require.NoError(t, err)
	require.NotEmpty(t, instID)
	require.Equal(t, []string{"fwd:a", "fwd:b", "fwd:c"}, rec.snapshot())

	inst, err := store.Get(context.Background(), instID)
	require.NoError(t, err)
	require.Equal(t, saga.StateCompleted, inst.State)
}

func TestDurableExecutor_ForwardFailureRunsCompensation(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	rec := &callRecorder{}
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "rollback-saga",
		Steps: []saga.DurableStep{
			passingStep("a", rec),
			passingStep("b", rec),
			failingForward("c", rec, errors.New("nope")),
		},
	}))

	instID, err := exec.Start(context.Background(), "rollback-saga", nil)
	var fwdErr *saga.ForwardError
	require.ErrorAs(t, err, &fwdErr)
	require.Equal(t, "c", fwdErr.Name)
	// Compensations run in reverse order of completed steps (a, b).
	require.Equal(t, []string{"fwd:a", "fwd:b", "fwd:c", "comp:b", "comp:a"}, rec.snapshot())

	inst, _ := store.Get(context.Background(), instID)
	require.Equal(t, saga.StateFailed, inst.State)
	require.ElementsMatch(t, []int{1, 0}, inst.Compensated)
}

// TestDurableExecutor_ResumeIsolatesStuckSaga proves that one saga blocked in a
// forward step does NOT prevent other resumable sagas from running to
// completion: Resume drives them concurrently. A regression here (sequential
// resume) would deadlock the test because the "fast" saga would never run until
// the "stuck" one returned, which it never does until released.
func TestDurableExecutor_ResumeIsolatesStuckSaga(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	rec := &callRecorder{}

	release := make(chan struct{})
	stuckEntered := make(chan struct{})
	stuckStep := saga.DurableStep{
		Name: "block",
		Forward: func(ctx context.Context, in []byte) ([]byte, error) {
			rec.record("fwd:block")
			close(stuckEntered)
			<-release // block until the test releases it
			return in, nil
		},
	}
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name:  "stuck",
		Steps: []saga.DurableStep{stuckStep},
	}))
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name:  "fast",
		Steps: []saga.DurableStep{passingStep("quick", rec)},
	}))

	ctx := context.Background()
	require.NoError(t, store.Put(ctx, saga.Instance{ID: "stuck-1", Definition: "stuck", State: saga.StateRunning}))
	require.NoError(t, store.Put(ctx, saga.Instance{ID: "fast-1", Definition: "fast", State: saga.StateRunning}))

	done := make(chan struct{})
	go func() {
		_, _ = exec.Resume(ctx, 0)
		close(done)
	}()

	// The stuck saga has entered its blocking step; the fast saga must still
	// complete while it is blocked (would hang under sequential resume).
	<-stuckEntered
	require.Eventually(t, func() bool {
		inst, err := store.Get(ctx, "fast-1")
		return err == nil && inst.State == saga.StateCompleted
	}, 2*time.Second, 5*time.Millisecond, "fast saga should complete while the stuck one blocks")

	close(release) // let the stuck saga finish so Resume returns cleanly
	<-done
}

// TestDurableExecutor_ResumePanicDoesNotCrashBatch proves that a user step that
// panics during Resume is converted into that instance's ResumeResult.Err rather
// than crashing the host process, and that a sibling resumable saga still runs to
// completion — i.e. one panicking step does not short-circuit the batch (the
// documented Resume contract).
func TestDurableExecutor_ResumePanicDoesNotCrashBatch(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	rec := &callRecorder{}

	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "panics",
		Steps: []saga.DurableStep{{
			Name: "boom",
			Forward: func(context.Context, []byte) ([]byte, error) {
				panic("secret-token-leak")
			},
		}},
	}))
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name:  "fast",
		Steps: []saga.DurableStep{passingStep("quick", rec)},
	}))

	ctx := context.Background()
	require.NoError(t, store.Put(ctx, saga.Instance{ID: "panic-1", Definition: "panics", State: saga.StateRunning}))
	require.NoError(t, store.Put(ctx, saga.Instance{ID: "fast-1", Definition: "fast", State: saga.StateRunning}))

	results, err := exec.Resume(ctx, 0)
	require.NoError(t, err)
	require.Len(t, results, 2)

	byID := map[string]error{}
	for _, r := range results {
		byID[r.InstanceID] = r.Err
	}
	require.Error(t, byID["panic-1"], "panicking instance must surface an error")
	require.NotContains(t, byID["panic-1"].Error(), "secret-token-leak",
		"raw panic value must not be exposed")
	require.NoError(t, byID["fast-1"], "sibling saga must still complete")

	fastInst, _ := store.Get(ctx, "fast-1")
	require.Equal(t, saga.StateCompleted, fastInst.State)
}

// TestDurableExecutor_ResumeStuckSagasReleasedByCtxCancel documents the
// concurrency bound's real contract (Resume is not a per-saga timeout): when at
// least resumeConcurrency sagas block in a step, every slot fills and Resume can
// only make progress again once those steps return. The documented mitigation is
// a cancellable ctx + ctx-respecting steps: cancelling unblocks them and lets
// Resume return. Without this contract honoured, Resume would hang forever.
func TestDurableExecutor_ResumeStuckSagasReleasedByCtxCancel(t *testing.T) {
	const stuck = 16 // >= resumeConcurrency, so all slots fill and the loop blocks

	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)

	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "ctx-block",
		Steps: []saga.DurableStep{{
			Name: "wait",
			Forward: func(ctx context.Context, in []byte) ([]byte, error) {
				<-ctx.Done() // honours cancellation
				return nil, ctx.Err()
			},
		}},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < stuck; i++ {
		require.NoError(t, store.Put(ctx, saga.Instance{
			ID:         "blocked-" + string(rune('a'+i)),
			Definition: "ctx-block",
			State:      saga.StateRunning,
		}))
	}

	done := make(chan struct{})
	go func() {
		_, _ = exec.Resume(ctx, 0)
		close(done)
	}()

	// Resume should still be running (slots saturated, sagas blocked on ctx).
	select {
	case <-done:
		t.Fatal("Resume returned before stuck sagas were released")
	case <-time.After(50 * time.Millisecond):
	}

	cancel() // release every stuck step via ctx cancellation
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Resume did not return after ctx cancellation released the stuck sagas")
	}
}

func TestDurableExecutor_ResumeAfterCrash(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	rec := &callRecorder{}

	// Simulate a crash by stopping the saga mid-flight: register a
	// definition where step 1 panics-as-error to bail. We then manually
	// rewrite state to look like "step 1 was about to run when we
	// crashed" and ask Resume to pick it up.
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "resumable",
		Steps: []saga.DurableStep{
			passingStep("a", rec),
			passingStep("b", rec),
			passingStep("c", rec),
		},
	}))

	// Hand-craft an Instance left mid-flight: step a (idx 0) is done.
	preCrash := saga.Instance{
		ID:          "crashed-inst",
		Definition:  "resumable",
		State:       saga.StateRunning,
		CurrentStep: 1,
		StepResults: []json.RawMessage{json.RawMessage(`"a-output"`)},
	}
	require.NoError(t, store.Put(context.Background(), preCrash))

	// Resume should run steps b and c.
	results, err := exec.Resume(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Equal(t, []string{"fwd:b", "fwd:c"}, rec.snapshot())

	inst, _ := store.Get(context.Background(), "crashed-inst")
	require.Equal(t, saga.StateCompleted, inst.State)
}

func TestDurableExecutor_ResumeContinuesCompensation(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	rec := &callRecorder{}
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "mid-compensate",
		Steps: []saga.DurableStep{
			passingStep("a", rec),
			passingStep("b", rec),
			passingStep("c", rec),
		},
	}))

	// State left mid-compensation: a + b + c forward done (CurrentStep=3),
	// but state flipped to compensating, with c already compensated.
	preCrash := saga.Instance{
		ID:          "crashed-comp",
		Definition:  "mid-compensate",
		State:       saga.StateCompensating,
		CurrentStep: 3,
		Compensated: []int{2},
		StepResults: []json.RawMessage{nil, nil, nil},
	}
	require.NoError(t, store.Put(context.Background(), preCrash))

	results, err := exec.Resume(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	// Only b and a should compensate; c was already done.
	require.Equal(t, []string{"comp:b", "comp:a"}, rec.snapshot())

	inst, _ := store.Get(context.Background(), "crashed-comp")
	require.Equal(t, saga.StateFailed, inst.State)
}

func TestDurableExecutor_CompensationIdempotentAcrossResume(t *testing.T) {
	// A compensation that's already in Compensated is not re-fired even
	// if Resume sees the instance again.
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	var compCount atomic.Int32
	def := &saga.DurableDefinition{
		Name: "idem-comp",
		Steps: []saga.DurableStep{
			{
				Name:    "x",
				Forward: func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
				Compensate: func(_ context.Context, _ []byte, _ []byte) error {
					compCount.Add(1)
					return nil
				},
			},
		},
	}
	require.NoError(t, exec.Register(def))

	preCrash := saga.Instance{
		ID:          "idem",
		Definition:  "idem-comp",
		State:       saga.StateCompensating,
		CurrentStep: 1,
		Compensated: []int{0}, // already done
	}
	require.NoError(t, store.Put(context.Background(), preCrash))

	_, err := exec.Resume(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, int32(0), compCount.Load(), "already-compensated step must not re-fire")
}

func TestDurableExecutor_InputThreadedThroughSteps(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	var observedInput []byte
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "input-saga",
		Steps: []saga.DurableStep{
			{
				Name: "a",
				Forward: func(_ context.Context, in []byte) ([]byte, error) {
					observedInput = append([]byte(nil), in...)
					return []byte(`{"step":"a"}`), nil
				},
			},
			{
				Name: "b",
				Forward: func(_ context.Context, in []byte) ([]byte, error) {
					require.Equal(t, `{"step":"a"}`, string(in))
					return nil, nil
				},
			},
		},
	}))

	_, err := exec.Start(context.Background(), "input-saga", map[string]string{"k": "v"})
	require.NoError(t, err)
	require.Equal(t, `{"k":"v"}`, string(observedInput))
}

func TestRegister_RejectsDuplicate(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	def := &saga.DurableDefinition{
		Name:  "dup",
		Steps: []saga.DurableStep{{Name: "x", Forward: func(_ context.Context, _ []byte) ([]byte, error) { return nil, nil }}},
	}
	require.NoError(t, exec.Register(def))
	require.Error(t, exec.Register(def))
}

func TestRegister_ValidatesDefinition(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	cases := []*saga.DurableDefinition{
		nil,
		{Name: "", Steps: []saga.DurableStep{{Name: "x", Forward: func(context.Context, []byte) ([]byte, error) { return nil, nil }}}},
		{Name: "no-steps"},
		{Name: "missing-name", Steps: []saga.DurableStep{{Forward: func(context.Context, []byte) ([]byte, error) { return nil, nil }}}},
		{Name: "missing-forward", Steps: []saga.DurableStep{{Name: "x"}}},
		{Name: "dup-step-name", Steps: []saga.DurableStep{
			{Name: "x", Forward: func(context.Context, []byte) ([]byte, error) { return nil, nil }},
			{Name: "x", Forward: func(context.Context, []byte) ([]byte, error) { return nil, nil }},
		}},
	}
	for i, c := range cases {
		require.Error(t, exec.Register(c), "case %d should fail", i)
	}
}

func TestStart_UnknownDefinition(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, _ := saga.NewDurableExecutor(store)
	_, err := exec.Start(context.Background(), "ghost", nil)
	require.ErrorIs(t, err, saga.ErrDefinitionNotFound)
}

func TestMemoryStateStore_Lifecycle(t *testing.T) {
	s := saga.NewMemoryStateStore()
	require.Equal(t, 0, s.Len())
	require.NoError(t, s.Put(context.Background(), saga.Instance{ID: "i1", Definition: "d"}))
	require.Equal(t, 1, s.Len())
	got, err := s.Get(context.Background(), "i1")
	require.NoError(t, err)
	require.False(t, got.CreatedAt.IsZero())
	require.NoError(t, s.Delete(context.Background(), "i1"))
	require.Equal(t, 0, s.Len())
	_, err = s.Get(context.Background(), "i1")
	require.ErrorIs(t, err, saga.ErrInstanceNotFound)
}

func TestMemoryStateStore_PutEmptyIDRejectsAsValidationError(t *testing.T) {
	s := saga.NewMemoryStateStore()

	err := s.Put(context.Background(), saga.Instance{Definition: "d"})

	// An ID-less Put is caller misuse, not a missing instance: it must
	// not be reported via the not-found sentinel, otherwise a caller's
	// errors.Is(err, ErrInstanceNotFound) misclassifies the bug.
	require.Error(t, err)
	require.NotErrorIs(t, err, saga.ErrInstanceNotFound)
	require.Contains(t, err.Error(), "Instance.ID")
	// The rejected write must not be persisted.
	require.Equal(t, 0, s.Len())
}

func TestDurableExecutor_StartPanicSurfacesAsError(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, err := saga.NewDurableExecutor(store)
	require.NoError(t, err)
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "panic-saga",
		Steps: []saga.DurableStep{{
			Name: "boom",
			Forward: func(context.Context, []byte) ([]byte, error) {
				panic("secret-token-xyz")
			},
		}},
	}))

	instID, err := exec.Start(context.Background(), "panic-saga", nil)
	require.Error(t, err)
	require.NotEmpty(t, instID)
	require.NotContains(t, err.Error(), "secret-token-xyz")

	inst, getErr := store.Get(context.Background(), instID)
	require.NoError(t, getErr)
	require.NotContains(t, inst.LastError, "secret-token-xyz")
}

func TestDurableExecutor_CompensationIgnoresCallerCancel(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, err := saga.NewDurableExecutor(store)
	require.NoError(t, err)

	var compensated atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "comp-ctx",
		Steps: []saga.DurableStep{
			{
				Name: "a",
				Forward: func(context.Context, []byte) ([]byte, error) {
					return []byte(`"a"`), nil
				},
				Compensate: func(ctx context.Context, _, _ []byte) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					compensated.Store(true)
					return nil
				},
			},
			{
				Name: "b",
				Forward: func(context.Context, []byte) ([]byte, error) {
					cancel() // cancel the caller ctx mid-flight
					return nil, errors.New("downstream secret=s3cr3t")
				},
			},
		},
	}))

	instID, err := exec.Start(ctx, "comp-ctx", nil)
	require.Error(t, err)
	require.True(t, compensated.Load(), "compensation must run despite cancelled parent")
	inst, getErr := store.Get(context.Background(), instID)
	require.NoError(t, getErr)
	require.NotContains(t, inst.LastError, "s3cr3t")
}

func TestDurableExecutor_ConcurrentRunSameInstanceDoesNotDoubleForward(t *testing.T) {
	store := saga.NewMemoryStateStore()
	exec, err := saga.NewDurableExecutor(store)
	require.NoError(t, err)

	var forwards atomic.Int32
	gate := make(chan struct{})
	require.NoError(t, exec.Register(&saga.DurableDefinition{
		Name: "once",
		Steps: []saga.DurableStep{{
			Name: "only",
			Forward: func(context.Context, []byte) ([]byte, error) {
				forwards.Add(1)
				<-gate
				return []byte(`"ok"`), nil
			},
		}},
	}))

	id := "inst-race"
	require.NoError(t, store.Put(context.Background(), saga.Instance{
		ID: id, Definition: "once", State: saga.StatePending,
	}))

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			errs <- exec.Run(context.Background(), id)
		}()
	}
	// Let both attempt to enter executeInstance; lock serializes them.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.Equal(t, int32(1), forwards.Load(), "per-instance lock must serialize Forward")
}
