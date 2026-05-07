package safecast

import (
	"math"
	"testing"
	"testing/quick"
)

func TestUint64ToInt64(t *testing.T) {
	cases := []struct {
		in   uint64
		want int64
	}{
		{0, 0},
		{1, 1},
		{math.MaxInt64, math.MaxInt64},
		{math.MaxInt64 + 1, math.MaxInt64}, // overflow → clamp
		{math.MaxUint64, math.MaxInt64},    // overflow → clamp
	}
	for _, c := range cases {
		if got := Uint64ToInt64(c.in); got != c.want {
			t.Errorf("Uint64ToInt64(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIntToInt32(t *testing.T) {
	cases := []struct {
		in   int
		want int32
	}{
		{0, 0},
		{1, 1},
		{-1, -1},
		{math.MaxInt32, math.MaxInt32},
		{math.MinInt32, math.MinInt32},
	}
	for _, c := range cases {
		if got := IntToInt32(c.in); got != c.want {
			t.Errorf("IntToInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}

	// 64-bit-platform overflow cases. Skip on 32-bit platforms where the
	// int identity already equals int32.
	if math.MaxInt > math.MaxInt32 {
		if got := IntToInt32(math.MaxInt32 + 1); got != math.MaxInt32 {
			t.Errorf("overflow → got %d, want MaxInt32", got)
		}
		if got := IntToInt32(math.MinInt32 - 1); got != math.MinInt32 {
			t.Errorf("underflow → got %d, want MinInt32", got)
		}
	}
}

func TestIntToUint32(t *testing.T) {
	cases := []struct {
		in   int
		want uint32
	}{
		{0, 0},
		{1, 1},
		{-1, 0}, // underflow → clamp to 0
		{-1000000, 0},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxUint32, math.MaxUint32},
	}
	for _, c := range cases {
		if got := IntToUint32(c.in); got != c.want {
			t.Errorf("IntToUint32(%d) = %d, want %d", c.in, got, c.want)
		}
	}

	if math.MaxInt > math.MaxUint32 {
		if got := IntToUint32(math.MaxUint32 + 1); got != math.MaxUint32 {
			t.Errorf("overflow → got %d, want MaxUint32", got)
		}
	}
}

// TestUint64ToInt64_property: result is always non-negative and equals min(v, MaxInt64).
func TestUint64ToInt64_property(t *testing.T) {
	prop := func(v uint64) bool {
		got := Uint64ToInt64(v)
		if got < 0 {
			return false
		}
		if v > math.MaxInt64 {
			return got == math.MaxInt64
		}
		return uint64(got) == v
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}

// TestIntToInt32_property: result is always within [MinInt32, MaxInt32] and
// equals v when v is in range.
func TestIntToInt32_property(t *testing.T) {
	prop := func(v int) bool {
		got := IntToInt32(v)
		if int(got) < math.MinInt32 || int(got) > math.MaxInt32 {
			return false
		}
		switch {
		case v > math.MaxInt32:
			return got == math.MaxInt32
		case v < math.MinInt32:
			return got == math.MinInt32
		default:
			return int(got) == v
		}
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}

// TestIntToUint32_property: result is always within [0, MaxUint32] and
// equals v when v is in range.
func TestIntToUint32_property(t *testing.T) {
	prop := func(v int) bool {
		got := IntToUint32(v)
		switch {
		case v < 0:
			return got == 0
		case uint64(v) > math.MaxUint32:
			return got == math.MaxUint32
		default:
			return uint64(got) == uint64(v)
		}
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}
