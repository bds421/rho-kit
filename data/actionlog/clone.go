package actionlog

import (
	"encoding/json"
	"reflect"
)

// Clone returns a copy of e with mutable metadata containers detached.
func (e Entry) Clone() Entry {
	e.Metadata = cloneMetadata(e.Metadata)
	return e
}

func cloneEntry(e Entry) Entry {
	return e.Clone()
}

// cloneMetadata deep-copies validated metadata. Metadata is restricted by
// walkMetadata to JSON-shaped values (map[string]any, []any, and primitives)
// — never pointers, arrays, or cycles — so a specialized type-switch clone
// is complete and far cheaper than reflection on the List hot path.
//
// Cycle / shared-reference handling is retained for defensive cloning of
// off-band SignEntry inputs that skip validMetadata.
func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	seen := make(map[cloneVisit]any)
	return cloneMapStringAny(m, seen)
}

// cloneVisit keys map/slice identities. For slices, len is part of the key
// so s and s[:1] (same element-0 pointer, different Len) clone independently.
type cloneVisit struct {
	kind int // 0=map, 1=slice
	ptr  uintptr
	len  int
}

func cloneMapStringAny(m map[string]any, seen map[cloneVisit]any) map[string]any {
	if m == nil {
		return nil
	}
	id := cloneVisit{kind: 0, ptr: reflect.ValueOf(m).Pointer()}
	if id.ptr != 0 {
		if c, ok := seen[id]; ok {
			return c.(map[string]any)
		}
	}
	out := make(map[string]any, len(m))
	if id.ptr != 0 {
		seen[id] = out
	}
	for k, v := range m {
		out[k] = cloneJSONValue(v, seen)
	}
	return out
}

func cloneAnySlice(s []any, seen map[cloneVisit]any) []any {
	if s == nil {
		return nil
	}
	id := cloneVisit{kind: 1, ptr: reflect.ValueOf(s).Pointer(), len: len(s)}
	if id.ptr != 0 {
		if c, ok := seen[id]; ok {
			return c.([]any)
		}
	}
	out := make([]any, len(s))
	if id.ptr != 0 {
		seen[id] = out
	}
	for i, v := range s {
		out[i] = cloneJSONValue(v, seen)
	}
	return out
}

func cloneJSONValue(v any, seen map[cloneVisit]any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return cloneMapStringAny(x, seen)
	case []any:
		return cloneAnySlice(x, seen)
	case string, bool,
		float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return x
	default:
		// Non-JSON types should not appear in validated metadata. Return
		// as-is rather than panicking so Clone remains best-effort for
		// off-band SignEntry inputs.
		return x
	}
}
