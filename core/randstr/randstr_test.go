package randstr

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRuneSequence_lengthMatchesRequest(t *testing.T) {
	tests := []int{0, 1, 8, 32, 256}
	for _, n := range tests {
		got, err := RuneSequence(n, AlphaNum)
		if err != nil {
			t.Fatalf("length %d: unexpected error: %v", n, err)
		}
		if utf8.RuneCountInString(got) != n {
			t.Errorf("length %d: got rune-count %d", n, utf8.RuneCountInString(got))
		}
	}
}

func TestRuneSequence_runesOnlyFromCharset(t *testing.T) {
	cases := []struct {
		name    string
		charset []rune
	}{
		{"AlphaNum", AlphaNum},
		{"AlphaLowerNum", AlphaLowerNum},
		{"AlphaUpperNum", AlphaUpperNum},
		{"AlphaNumNoAmbiguous", AlphaNumNoAmbiguous},
		{"Numeric", Numeric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := make(map[rune]struct{}, len(tc.charset))
			for _, r := range tc.charset {
				set[r] = struct{}{}
			}
			got, err := RuneSequence(512, tc.charset)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, r := range got {
				if _, ok := set[r]; !ok {
					t.Errorf("rune %q not in charset", r)
				}
			}
		})
	}
}

func TestAlphaNumNoAmbiguous_excludesVisuallyAmbiguous(t *testing.T) {
	for _, r := range "0OIl1" {
		for _, c := range AlphaNumNoAmbiguous {
			if c == r {
				t.Errorf("AlphaNumNoAmbiguous must not contain %q", r)
			}
		}
	}
}

func TestRuneSequence_invalidArgs(t *testing.T) {
	if _, err := RuneSequence(-1, AlphaNum); err == nil {
		t.Error("expected error for negative length")
	}
	if _, err := RuneSequence(8, nil); err == nil {
		t.Error("expected error for nil charset")
	}
	if _, err := RuneSequence(8, []rune{}); err == nil {
		t.Error("expected error for empty charset")
	}
}

func TestMustString_panicsOnInvalidArgs(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for empty charset")
		}
	}()
	_ = MustString(8, []rune{})
}

func TestMustString_returnsString(t *testing.T) {
	got := MustString(16, AlphaNum)
	if len(got) != 16 {
		t.Errorf("length = %d, want 16", len(got))
	}
}

// TestRuneSequence_distributionSanity draws a large sample and asserts every
// rune in the charset appears within [50%, 150%] of its expected frequency.
// This is a coarse statistical check that the rejection sampler is uniform —
// not a tight chi-squared test (which would flake under the random seed).
func TestRuneSequence_distributionSanity(t *testing.T) {
	const samples = 10000
	charset := AlphaNum
	got, err := RuneSequence(samples, charset)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts := make(map[rune]int, len(charset))
	for _, r := range got {
		counts[r]++
	}

	expected := float64(samples) / float64(len(charset))
	low := expected * 0.5
	high := expected * 1.5
	for _, r := range charset {
		c := float64(counts[r])
		if c < low || c > high {
			t.Errorf("rune %q: count %.0f outside [%.0f, %.0f] (expected ≈ %.0f)", r, c, low, high, expected)
		}
	}
}

func TestRuneSequence_unicodeCharset(t *testing.T) {
	// Confirm the function handles multi-byte runes correctly (each output
	// position is a single rune drawn from the set, regardless of byte width).
	charset := []rune("αβγδε")
	got, err := RuneSequence(64, charset)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf8.RuneCountInString(got) != 64 {
		t.Errorf("rune count = %d, want 64", utf8.RuneCountInString(got))
	}
	allowed := string(charset)
	for _, r := range got {
		if !strings.ContainsRune(allowed, r) {
			t.Errorf("rune %q not in charset", r)
		}
	}
}
