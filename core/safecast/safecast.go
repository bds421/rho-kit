// Package safecast provides bounded integer conversions. Two variants
// per cast:
//
//   - Saturating helpers (Uint64ToInt64, IntToInt32, IntToUint32, …):
//     clamp to the destination's range. Convenient for display and
//     metrics labels where exact representation isn't required.
//   - Try* helpers (TryUint64ToInt64, …): return (value, ok) so callers
//     can detect truncation explicitly. Use these whenever the value
//     propagates to a wire format (timestamps, sizes, billing totals)
//     where silent saturation would be a correctness bug.
package safecast

import "math"

// Uint64ToInt64 narrows v to int64, clamping to [math.MaxInt64] on overflow.
// Negative values are not possible from a uint64 input, so no lower bound is
// applied.
func Uint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// TryUint64ToInt64 narrows v to int64. Returns (value, true) when v fits;
// (math.MaxInt64, false) when v overflows.
func TryUint64ToInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return math.MaxInt64, false
	}
	return int64(v), true
}

// IntToInt32 narrows v to int32, clamping to [math.MaxInt32] on overflow
// and [math.MinInt32] on underflow.
func IntToInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// TryIntToInt32 narrows v to int32, returning (value, true) when v fits
// and (clamped, false) on overflow / underflow.
func TryIntToInt32(v int) (int32, bool) {
	if v > math.MaxInt32 {
		return math.MaxInt32, false
	}
	if v < math.MinInt32 {
		return math.MinInt32, false
	}
	return int32(v), true
}

// IntToUint32 narrows v to uint32, clamping to [math.MaxUint32] on overflow
// and 0 on negative input.
func IntToUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if uint64(v) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}

// TryIntToUint32 narrows v to uint32. Returns (value, true) when v fits;
// (clamped, false) on overflow / negative input.
func TryIntToUint32(v int) (uint32, bool) {
	if v < 0 {
		return 0, false
	}
	if uint64(v) > math.MaxUint32 {
		return math.MaxUint32, false
	}
	return uint32(v), true
}

// Int64ToInt32 narrows v to int32, clamping on overflow / underflow.
func Int64ToInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// TryInt64ToInt32 narrows v to int32. Returns (value, true) when v fits;
// (clamped, false) on overflow / underflow.
func TryInt64ToInt32(v int64) (int32, bool) {
	if v > math.MaxInt32 {
		return math.MaxInt32, false
	}
	if v < math.MinInt32 {
		return math.MinInt32, false
	}
	return int32(v), true
}

// IntToUint64 narrows v to uint64, clamping 0 on negative input.
func IntToUint64(v int) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// TryIntToUint64 narrows v to uint64. Returns (value, true) when v >= 0;
// (0, false) on negative input.
func TryIntToUint64(v int) (uint64, bool) {
	if v < 0 {
		return 0, false
	}
	return uint64(v), true
}
