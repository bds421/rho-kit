package acceptencoding

import "testing"

func TestSelect(t *testing.T) {
	offered := []string{"br", "gzip"}
	cases := []struct {
		header, want string
	}{
		{"", ""},
		{"identity", ""},
		{"gzip", "gzip"},
		{"gzip, br", "br"},
		{"gzip;q=1, br;q=0.5", "gzip"},
		{"br;q=0, gzip", "gzip"},
		{"BR", "br"},
		{"*", "br"},
		{"br;q=0, *", "gzip"},
		{"br;q=-1, gzip;q=0", ""},
		{"zstd", ""},
	}
	for _, c := range cases {
		if got := Select(c.header, offered); got != c.want {
			t.Errorf("Select(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
