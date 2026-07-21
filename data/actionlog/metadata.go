package actionlog

import (
	"encoding/json"
	"math"
	"reflect"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxMetadataBytes caps the canonical JSON form stored and signed for an
	// entry's Metadata field.
	MaxMetadataBytes = 8 * 1024

	// MaxMetadataEntries caps the total number of object keys across metadata.
	MaxMetadataEntries = 64

	// MaxMetadataKeyLen caps each metadata object key.
	MaxMetadataKeyLen = 128

	// MaxMetadataArrayLen caps a single metadata array.
	MaxMetadataArrayLen = 64

	// MaxMetadataDepth caps nested metadata maps/arrays.
	MaxMetadataDepth = 8

	maxMetadataNodes = 256
)

type metadataStats struct {
	entries int
	nodes   int
}

type metadataVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func validMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return true
	}
	stats := metadataStats{}
	if !walkMetadata(metadata, 0, make(map[metadataVisit]struct{}), &stats) {
		return false
	}
	raw, err := canonicalJSON(metadata)
	return err == nil && len(raw) <= MaxMetadataBytes
}

func walkMetadata(value any, depth int, seen map[metadataVisit]struct{}, stats *metadataStats) bool {
	if depth > MaxMetadataDepth {
		return false
	}
	stats.nodes++
	if stats.nodes > maxMetadataNodes {
		return false
	}

	switch v := value.(type) {
	case nil:
		return true
	case string:
		return validMetadataString(v)
	case bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return true
	case float32:
		f := float64(v)
		return !math.IsNaN(f) && !math.IsInf(f, 0)
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case map[string]any:
		return walkMetadataMap(v, depth, seen, stats)
	case []any:
		return walkMetadataSlice(v, depth, seen, stats)
	default:
		return false
	}
}

func walkMetadataMap(m map[string]any, depth int, seen map[metadataVisit]struct{}, stats *metadataStats) bool {
	if m == nil {
		return true
	}
	id := metadataVisit{kind: reflect.Map, ptr: reflect.ValueOf(m).Pointer()}
	if id.ptr != 0 {
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
		defer delete(seen, id)
	}

	stats.entries += len(m)
	if stats.entries > MaxMetadataEntries {
		return false
	}
	for k, v := range m {
		if !validMetadataKey(k) || !walkMetadata(v, depth+1, seen, stats) {
			return false
		}
	}
	return true
}

func walkMetadataSlice(values []any, depth int, seen map[metadataVisit]struct{}, stats *metadataStats) bool {
	if values == nil {
		return true
	}
	if len(values) > MaxMetadataArrayLen {
		return false
	}
	id := metadataVisit{kind: reflect.Slice, ptr: reflect.ValueOf(values).Pointer()}
	if id.ptr != 0 {
		if _, ok := seen[id]; ok {
			return false
		}
		seen[id] = struct{}{}
		defer delete(seen, id)
	}
	for _, v := range values {
		if !walkMetadata(v, depth+1, seen, stats) {
			return false
		}
	}
	return true
}

func validMetadataKey(key string) bool {
	if key == "" || len(key) > MaxMetadataKeyLen || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validMetadataString(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
