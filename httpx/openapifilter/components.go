package openapifilter

import (
	"encoding/json"
	"regexp"

	"github.com/getkin/kin-openapi/openapi3"
)

var componentRef = regexp.MustCompile(`"#/components/(schemas|parameters|responses|requestBodies)/([^"]+)"`)

// dropUnreferenced removes the components reached before the filter and
// not after. The loaded document keeps each reference's name, so the
// document as served still says where they point.
func dropUnreferenced(spec *openapi3.T, before map[string]bool) error {
	reached, err := reachedComponents(spec)
	if err != nil {
		return err
	}
	gone := func(key string) bool { return before[key] && !reached[key] }
	c := spec.Components
	if c == nil {
		return nil
	}
	dropGone(c.Schemas, "schemas", gone)
	dropGone(c.Parameters, "parameters", gone)
	dropGone(c.Responses, "responses", gone)
	dropGone(c.RequestBodies, "requestBodies", gone)
	return nil
}

// reachedComponents are the components the paths refer to, as kind/name.
func reachedComponents(spec *openapi3.T) (map[string]bool, error) {
	reached := map[string]bool{}
	pending := []any{spec.Paths}
	for len(pending) > 0 {
		doc, err := json.Marshal(pending[0])
		if err != nil {
			return nil, err
		}
		pending = pending[1:]
		for _, m := range componentRef.FindAllStringSubmatch(string(doc), -1) {
			key := m[1] + "/" + m[2]
			if reached[key] {
				continue
			}
			reached[key] = true
			if c := component(spec.Components, m[1], m[2]); c != nil {
				pending = append(pending, c)
			}
		}
	}
	return reached, nil
}

func dropGone[V any](components map[string]V, kind string, gone func(string) bool) {
	for name := range components {
		if gone(kind + "/" + name) {
			delete(components, name)
		}
	}
}

func component(c *openapi3.Components, kind, name string) any {
	if c == nil {
		return nil
	}
	switch kind {
	case "schemas":
		if s := c.Schemas[name]; s != nil {
			return s.Value
		}
	case "parameters":
		if s := c.Parameters[name]; s != nil {
			return s.Value
		}
	case "responses":
		if s := c.Responses[name]; s != nil {
			return s.Value
		}
	case "requestBodies":
		if s := c.RequestBodies[name]; s != nil {
			return s.Value
		}
	}
	return nil
}
