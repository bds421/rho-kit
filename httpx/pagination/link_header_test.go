package pagination

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// parseLinkHeader parses the comma-separated RFC 5988 Link header into a map
// of rel → target URL. It is a focused parser: enough to verify the writer's
// output, not a general-purpose Link parser.
func parseLinkHeader(t *testing.T, header string) map[string]string {
	t.Helper()
	if header == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	// Split on ", " between links. Each link starts with "<".
	for _, raw := range splitTopLevel(header, ',') {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "<") {
			t.Fatalf("malformed link: %q", raw)
		}
		end := strings.Index(raw, ">")
		if end < 0 {
			t.Fatalf("missing '>' in: %q", raw)
		}
		target := raw[1:end]
		// Find rel="..."
		params := raw[end+1:]
		var rel string
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "rel=") {
				continue
			}
			rel = strings.Trim(p[len("rel="):], `"`)
		}
		if rel == "" {
			t.Fatalf("missing rel: %q", raw)
		}
		out[rel] = target
	}
	return out
}

// splitTopLevel splits on sep but ignores separators inside <...>.
func splitTopLevel(s string, sep rune) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

func TestWriteLinkHeader_firstPageEmitsNextAndLast(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 100, 0, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))

	if _, ok := links["prev"]; ok {
		t.Error("prev should be omitted on first page")
	}
	if _, ok := links["first"]; ok {
		t.Error("first should be omitted on first page")
	}

	wantNext := "https://example.com/items?limit=10&offset=10"
	if links["next"] != wantNext {
		t.Errorf("next = %q, want %q", links["next"], wantNext)
	}
	wantLast := "https://example.com/items?limit=10&offset=90"
	if links["last"] != wantLast {
		t.Errorf("last = %q, want %q", links["last"], wantLast)
	}
}

func TestWriteLinkHeader_lastPageOmitsNext(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 100, 90, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))

	if _, ok := links["next"]; ok {
		t.Error("next should be omitted on last page")
	}
	if _, ok := links["last"]; ok {
		t.Error("last should be omitted on last page")
	}

	wantPrev := "https://example.com/items?limit=10&offset=80"
	if links["prev"] != wantPrev {
		t.Errorf("prev = %q, want %q", links["prev"], wantPrev)
	}
	wantFirst := "https://example.com/items?limit=10&offset=0"
	if links["first"] != wantFirst {
		t.Errorf("first = %q, want %q", links["first"], wantFirst)
	}
}

func TestWriteLinkHeader_middlePageEmitsAllFour(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 100, 30, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))

	for _, rel := range []string{"first", "prev", "next", "last"} {
		if _, ok := links[rel]; !ok {
			t.Errorf("expected rel=%q", rel)
		}
	}
	if links["prev"] != "https://example.com/items?limit=10&offset=20" {
		t.Errorf("prev = %q", links["prev"])
	}
	if links["next"] != "https://example.com/items?limit=10&offset=40" {
		t.Errorf("next = %q", links["next"])
	}
}

func TestWriteLinkHeader_offsetBeyondTotalOmitsNextAndLast(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 50, 100, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))

	if _, ok := links["next"]; ok {
		t.Error("next should be omitted when offset > total")
	}
	if _, ok := links["last"]; ok {
		t.Error("last should be omitted when offset > total")
	}
	// prev/first still emitted because offset > 0.
	if _, ok := links["first"]; !ok {
		t.Error("first should be emitted")
	}
	if _, ok := links["prev"]; !ok {
		t.Error("prev should be emitted")
	}
}

func TestWriteLinkHeader_totalZero(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 0, 0, 10)

	got := w.Header().Get("Link")
	if got != "" {
		t.Errorf("expected no Link header, got %q", got)
	}
}

func TestWriteLinkHeader_unknownTotalEmitsOnlyNext(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, -1, 20, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))

	if len(links) != 1 {
		t.Errorf("expected exactly 1 link, got %v", links)
	}
	if links["next"] != "https://example.com/items?limit=10&offset=30" {
		t.Errorf("next = %q", links["next"])
	}
}

func TestWriteLinkHeader_lastPageOffsetMath(t *testing.T) {
	cases := []struct {
		total, limit, want int
	}{
		{100, 10, 90}, // 10 full pages: last starts at 90
		{101, 10, 100},
		{99, 10, 90},
		{10, 10, 0},
		{1, 10, 0},
		{27, 10, 20},
	}
	for _, c := range cases {
		got := lastPageOffset(c.total, c.limit)
		if got != c.want {
			t.Errorf("lastPageOffset(%d, %d) = %d, want %d", c.total, c.limit, got, c.want)
		}
	}
}

func TestWriteLinkHeader_preservesExistingQueryParams(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items?status=active&limit=99&offset=99")
	WriteLinkHeader(w, u, 100, 30, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))
	for rel, target := range links {
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatalf("rel=%q: %v", rel, err)
		}
		q := parsed.Query()
		if q.Get("status") != "active" {
			t.Errorf("rel=%q: status param lost", rel)
		}
	}
}

func TestWriteLinkHeader_invalidLimitNoOp(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 100, 0, 0)
	if got := w.Header().Get("Link"); got != "" {
		t.Errorf("expected empty header, got %q", got)
	}
}

func TestWriteLinkHeader_negativeOffsetTreatedAsZero(t *testing.T) {
	w := httptest.NewRecorder()
	u, _ := url.Parse("https://example.com/items")
	WriteLinkHeader(w, u, 100, -5, 10)

	links := parseLinkHeader(t, w.Header().Get("Link"))
	if _, ok := links["prev"]; ok {
		t.Error("prev should be omitted when offset clamps to 0")
	}
	if links["next"] != "https://example.com/items?limit=10&offset=10" {
		t.Errorf("next = %q", links["next"])
	}
}
