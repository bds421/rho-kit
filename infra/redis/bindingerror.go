package redis

import "fmt"

// BindingError describes a validation failure for one entry in a bindings
// slice (empty name, nil handler, etc.). Historically returned by
// StartProcessors/StartConsumers-style helpers; retained for callers that
// still type-assert or construct it when validating binding lists.
// Prefer returning a plain fmt.Errorf from new APIs unless index-aware
// diagnostics are required.
type BindingError struct {
	Index  int
	Reason string
}

func (e *BindingError) Error() string {
	return fmt.Sprintf("binding [%d]: %s", e.Index, e.Reason)
}
