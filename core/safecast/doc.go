// Package safecast provides clamping integer-narrowing helpers.
//
// Go's untyped integer conversions silently wrap on overflow:
//
//	int64(uint64(math.MaxUint64)) // -1
//
// gosec rule G115 (integer-overflow conversion) flags every such
// conversion at lint time. Use these helpers instead — they clamp the
// result to the destination type's bound rather than wrapping.
//
// # Original implementation
//
// Modeled on github.com/ory/x/safecast (Apache-2.0). The Ory variant ships
// only [Uint64ToInt64]; this package extends with [IntToInt32] and
// [IntToUint32] for the common 32-bit narrowings.
package safecast
