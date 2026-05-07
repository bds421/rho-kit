// Package safecast provides clamping integer-narrowing helpers.
//
// Go's untyped integer conversions silently wrap on overflow:
//
//	int64(uint64(math.MaxUint64)) // -1
//
// gosec rule G115 (integer-overflow conversion) flags every such
// conversion at lint time. Use these helpers instead — they clamp the
// result to the destination type's bound rather than wrapping.
package safecast
