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
