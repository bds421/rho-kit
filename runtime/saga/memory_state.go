package saga

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// MemoryStateStore is an in-process [StateStore]. Use for tests and
// single-process services that prefer "the executor runs as long as
// the process does" semantics over crash recovery. Production
// deployments needing resume-on-crash should use a durable backend
// such as data/saga/pgstore.
type MemoryStateStore struct {
	mu        sync.Mutex
	instances map[string]Instance
	now       func() time.Time
}

// NewMemoryStateStore returns a fresh, empty store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{
		instances: make(map[string]Instance),
		now:       time.Now,
	}
}

// Put implements [StateStore].
func (m *MemoryStateStore) Put(_ context.Context, inst Instance) error {
	if inst.ID == "" {
		// Misuse: an ID-less Put is a programmer bug, not a missing
		// instance. Returning ErrInstanceNotFound here would make a
		// caller's errors.Is(err, ErrInstanceNotFound) misclassify the
		// validation failure as "instance absent". Mirror the pgstore
		// backend, which returns a distinct validation error.
		return errors.New("saga: Put requires a non-empty Instance.ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if existing, ok := m.instances[inst.ID]; ok {
		inst.CreatedAt = existing.CreatedAt
	} else {
		inst.CreatedAt = now
	}
	inst.UpdatedAt = now
	m.instances[inst.ID] = cloneInstance(inst)
	return nil
}

// Get implements [StateStore].
func (m *MemoryStateStore) Get(_ context.Context, id string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[id]
	if !ok {
		return Instance{}, ErrInstanceNotFound
	}
	return cloneInstance(inst), nil
}

// ListResumable implements [StateStore].
func (m *MemoryStateStore) ListResumable(_ context.Context, olderThan time.Duration) ([]Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	threshold := m.now().Add(-olderThan)
	out := make([]Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		if inst.IsTerminal() {
			continue
		}
		if olderThan > 0 && inst.UpdatedAt.After(threshold) {
			continue
		}
		out = append(out, cloneInstance(inst))
	}
	return out, nil
}

// Delete implements [StateStore]. Idempotent.
func (m *MemoryStateStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.instances, id)
	return nil
}

// Len returns the count of stored instances (useful in tests).
func (m *MemoryStateStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.instances)
}

func cloneInstance(in Instance) Instance {
	out := in
	if in.Input != nil {
		out.Input = append([]byte(nil), in.Input...)
	}
	if in.Compensated != nil {
		out.Compensated = append([]int(nil), in.Compensated...)
	}
	if in.StepResults != nil {
		out.StepResults = make([]json.RawMessage, len(in.StepResults))
		for i, r := range in.StepResults {
			out.StepResults[i] = append([]byte(nil), r...)
		}
	}
	return out
}
