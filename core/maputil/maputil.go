// Package maputil provides small, allocation-light helpers for working
// with maps. Members live here when they are general enough to be used
// by multiple unrelated callers (httpx, data adapters, app builder, …)
// rather than belonging to any one of them.
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
