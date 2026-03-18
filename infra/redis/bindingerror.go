package redis

import "fmt"

// BindingError is returned by Start* functions when a binding at the given
// index fails validation (e.g. empty name, nil handler).
type BindingError struct {
	Index  int
	Reason string
}

func (e *BindingError) Error() string {
	return fmt.Sprintf("binding [%d]: %s", e.Index, e.Reason)
}
