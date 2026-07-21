// Package maputil provides small, allocation-light helpers for working
// with maps at service call sites (partial-update request bodies,
// optional-pointer field patches). The helpers are intentionally
// freestanding so application code can import them without pulling a
// heavier domain package; the kit itself keeps few call sites so the
// surface stays tiny and stable.
package maputil

// SetIfNotNil writes the dereferenced value of val into m at key when val
// is non-nil. It is the canonical building block for partial-update maps
// where each request field is an optional pointer.
//
// Passing a nil map panics — the helper is intentionally strict so a
// misuse surfaces at the call site rather than silently dropping fields.
func SetIfNotNil[T any](m map[string]any, key string, val *T) {
	if val == nil {
		return
	}
	if m == nil {
		panic("maputil: SetIfNotNil called with a nil map")
	}
	m[key] = *val
}
