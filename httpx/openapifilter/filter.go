package openapifilter

import (
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// Filter keeps the operations whose Extension value Keep accepts. An
// operation without the extension is always kept; one whose value is not
// a string is never kept.
type Filter struct {
	Extension string
	Keep      func(value string) bool
}

// Keeps tells whether the filter keeps an operation.
func (f Filter) Keeps(op *openapi3.Operation) bool {
	raw, marked := op.Extensions[f.Extension]
	if !marked {
		return true
	}
	value, ok := raw.(string)
	return ok && f.Keep(value)
}

// Spec reads a contract through get and returns it filtered.
func (f Filter) Spec(get func() (*openapi3.T, error)) func() (*openapi3.T, error) {
	return func() (*openapi3.T, error) {
		spec, err := get()
		if err != nil {
			return nil, err
		}
		return spec, f.Apply(spec)
	}
}

// Apply removes from spec the operations the filter does not keep, the
// paths left without one, the tags no kept operation names and the
// components only the removed operations reached, directly or through
// other components. A component no path ever reached (a webhook's
// payload) stays.
func (f Filter) Apply(spec *openapi3.T) error {
	before, err := reachedComponents(spec)
	if err != nil {
		return err
	}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if !f.Keeps(op) {
				item.SetOperation(method, nil)
			}
		}
		if len(item.Operations()) == 0 {
			spec.Paths.Delete(path)
		}
	}
	dropUnusedTags(spec)
	return dropUnreferenced(spec, before)
}

// Unknown lists the operations whose Extension value is not one of known,
// so a test can hold a contract's marks to the values its filter decides.
func Unknown(spec *openapi3.T, extension string, known ...string) []string {
	var out []string
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			raw, marked := op.Extensions[extension]
			if !marked {
				continue
			}
			if value, ok := raw.(string); !ok || !slices.Contains(known, value) {
				out = append(out, op.OperationID)
			}
		}
	}
	return out
}

func dropUnusedTags(spec *openapi3.T) {
	used := map[string]bool{}
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			for _, t := range op.Tags {
				used[t] = true
			}
		}
	}
	kept := spec.Tags[:0]
	for _, t := range spec.Tags {
		if used[t.Name] {
			kept = append(kept, t)
		}
	}
	spec.Tags = kept
}
