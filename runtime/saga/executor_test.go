package saga_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

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
