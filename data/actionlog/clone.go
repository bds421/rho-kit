package actionlog

import "reflect"

type cloneVisit struct {
	typ reflect.Type
	ptr uintptr
}

// Clone returns a copy of e with mutable metadata containers detached.
func (e Entry) Clone() Entry {
	e.Metadata = cloneMetadata(e.Metadata)
	return e
}

func cloneEntry(e Entry) Entry {
	return e.Clone()
}

func cloneMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cloned := cloneValue(reflect.ValueOf(m), make(map[cloneVisit]reflect.Value))
	if !cloned.IsValid() || cloned.IsNil() {
		return nil
	}
	out, _ := cloned.Interface().(map[string]any)
	return out
}

func cloneValue(v reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		cloned := cloneValue(v.Elem(), seen)
		out := reflect.New(v.Type()).Elem()
		out.Set(cloned)
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		id := cloneVisit{typ: v.Type(), ptr: v.Pointer()}
		if id.ptr != 0 {
			if cloned, ok := seen[id]; ok {
				return cloned
			}
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		if id.ptr != 0 {
			seen[id] = out
		}
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneValue(iter.Key(), seen), cloneValue(iter.Value(), seen))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		id := cloneVisit{typ: v.Type(), ptr: v.Pointer()}
		if id.ptr != 0 {
			if cloned, ok := seen[id]; ok {
				return cloned
			}
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if id.ptr != 0 {
			seen[id] = out
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i), seen))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i), seen))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		id := cloneVisit{typ: v.Type(), ptr: v.Pointer()}
		if id.ptr != 0 {
			if cloned, ok := seen[id]; ok {
				return cloned
			}
		}
		out := reflect.New(v.Type().Elem())
		if id.ptr != 0 {
			seen[id] = out
		}
		out.Elem().Set(cloneValue(v.Elem(), seen))
		return out
	default:
		return v
	}
}
