package urlutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestMustJoin_basic(t *testing.T) {
	got := MustJoin("https://example.com", "v1", "users")
	want := "https://example.com/v1/users"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMustJoin_preservesTrailingSlash(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		parts []string
		want  string
	}{
		{"base trailing slash, no parts", "https://example.com/", nil, "https://example.com/"},
		{"base trailing slash, part without", "https://example.com/api/", []string{"v1"}, "https://example.com/api/v1"},
		{"final part trailing slash", "https://example.com/api", []string{"v1/"}, "https://example.com/api/v1/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MustJoin(tc.base, tc.parts...)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMustJoin_preservesQueryAndFragment(t *testing.T) {
	got := MustJoin("https://example.com/api?token=abc&x=1#frag", "v1", "users")
	want := "https://example.com/api/v1/users?token=abc&x=1#frag"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMustJoin_panicsOnInvalidBase(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Error("expected panic on invalid base")
		}
		got := rec.(string)
		if got != "urlutil: MustJoin: base URL is invalid" {
			t.Fatalf("panic = %q, want stable invalid-base message", got)
		}
		if strings.Contains(got, "secret-token") || strings.Contains(got, "%zz") {
			t.Fatalf("panic leaked raw base URL: %q", got)
		}
	}()
	_ = MustJoin("https://example.com/%zz?token=secret-token")
}

func TestAppendPaths_doesNotMutateInput(t *testing.T) {
	in, err := url.Parse("https://example.com/api?x=1")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := in.Path
	originalQuery := in.RawQuery

	_ = AppendPaths(in, "v1", "users")

	if in.Path != originalPath {
		t.Errorf("input Path mutated: %q != %q", in.Path, originalPath)
	}
	if in.RawQuery != originalQuery {
		t.Errorf("input RawQuery mutated: %q != %q", in.RawQuery, originalQuery)
	}
}

func TestAppendPaths_idempotentAcrossSplit(t *testing.T) {
	base, err := url.Parse("https://example.com/api?token=abc#frag")
	if err != nil {
		t.Fatal(err)
	}

	// AppendPaths(u, "a", "b") should equal AppendPaths(AppendPaths(u, "a"), "b")
	// after URL-comparison.
	combined := AppendPaths(base, "a", "b")
	stepwise := AppendPaths(AppendPaths(base, "a"), "b")

	if combined.String() != stepwise.String() {
		t.Errorf("not idempotent: %q != %q", combined.String(), stepwise.String())
	}
}

func TestAppendPaths_emptyPartsSkipped(t *testing.T) {
	base := mustParse(t, "https://example.com/api")
	got := AppendPaths(base, "", "v1", "", "users", "")
	want := "https://example.com/api/v1/users"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_noPartsKeepsTrailingSlash(t *testing.T) {
	base := mustParse(t, "https://example.com/api/")
	got := AppendPaths(base)
	want := "https://example.com/api/"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_slashOnlyPartCreatesRootPath(t *testing.T) {
	base := mustParse(t, "https://example.com")
	got := AppendPaths(base, "/")
	want := "https://example.com/"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_noDoubleEncoding(t *testing.T) {
	// A part containing "%20" (already percent-encoded space) must not be
	// re-encoded into "%2520".
	base := mustParse(t, "https://example.com/api")
	got := AppendPaths(base, "with%20space")
	want := "https://example.com/api/with%20space"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_treatsPartsAsOpaqueSegments(t *testing.T) {
	base := mustParse(t, "https://example.com/api")
	got := AppendPaths(base, "tenant/acme", "users")
	want := "https://example.com/api/tenant%2Facme/users"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_doesNotCleanDotSegments(t *testing.T) {
	base := mustParse(t, "https://example.com/api/base")
	got := AppendPaths(base, "..", "../admin", ".")
	want := "https://example.com/api/base/%2E%2E/%2E%2E%2Fadmin/%2E"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_leadingSlashesDoNotReplaceBasePath(t *testing.T) {
	base := mustParse(t, "https://example.com/api")
	got := AppendPaths(base, "//evil.example/internal")
	want := "https://example.com/api/evil.example%2Finternal"
	if got.String() != want {
		t.Errorf("got %q, want %q", got.String(), want)
	}
}

func TestAppendPaths_reencodesEncodedPathSeparators(t *testing.T) {
	base := mustParse(t, "https://example.com/api")
	cases := []struct {
		name string
		part string
		want string
	}{
		{
			name: "encoded slash",
			part: "tenant%2Facme",
			want: "https://example.com/api/tenant%252Facme",
		},
		{
			name: "encoded backslash",
			part: "tenant%5Cacme",
			want: "https://example.com/api/tenant%255Cacme",
		},
		{
			name: "lowercase encoded slash",
			part: "tenant%2facme",
			want: "https://example.com/api/tenant%252facme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendPaths(base, tc.part)
			if got.String() != tc.want {
				t.Errorf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestAppendPaths_reencodesEncodedDotSegments(t *testing.T) {
	base := mustParse(t, "https://example.com/api/base")
	cases := []struct {
		name string
		part string
		want string
	}{
		{
			name: "encoded single dot",
			part: "%2e",
			want: "https://example.com/api/base/%252e",
		},
		{
			name: "encoded double dot",
			part: "%2e%2e",
			want: "https://example.com/api/base/%252e%252e",
		},
		{
			name: "encoded slash creates dot segment",
			part: "tenant%2f%2e%2e%2fadmin",
			want: "https://example.com/api/base/tenant%252f%252e%252e%252fadmin",
		},
		{
			name: "mixed case encoded dot",
			part: "%2E%2e",
			want: "https://example.com/api/base/%252E%252e",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendPaths(base, tc.part)
			if got.String() != tc.want {
				t.Errorf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestAppendPaths_preservesQueryAndFragment(t *testing.T) {
	base := mustParse(t, "https://example.com/api?token=abc&x=1#frag")
	got := AppendPaths(base, "v1", "users")
	if got.RawQuery != "token=abc&x=1" {
		t.Errorf("RawQuery = %q, want %q", got.RawQuery, "token=abc&x=1")
	}
	if got.Fragment != "frag" {
		t.Errorf("Fragment = %q, want %q", got.Fragment, "frag")
	}
}

func TestAppendPaths_nilSafe(t *testing.T) {
	if got := AppendPaths(nil, "x"); got != nil {
		t.Errorf("AppendPaths(nil, ...) = %v, want nil", got)
	}
}

func TestCopy_isDeep(t *testing.T) {
	in := mustParse(t, "https://user:pass@example.com/p?x=1#f")
	out := Copy(in)

	if out == in {
		t.Error("Copy returned the same pointer")
	}
	if out.String() != in.String() {
		t.Errorf("Copy mismatch: %q != %q", out.String(), in.String())
	}

	// Mutating the copy must not touch the input.
	out.Path = "/changed"
	if in.Path == "/changed" {
		t.Error("mutating copy affected input")
	}

	// User field copied (not aliased).
	pwIn, _ := in.User.Password()
	pwOut, _ := out.User.Password()
	if pwIn != pwOut || in.User.Username() != out.User.Username() {
		t.Error("User credentials not copied")
	}
}

func TestCopy_nilSafe(t *testing.T) {
	if got := Copy(nil); got != nil {
		t.Errorf("Copy(nil) = %v, want nil", got)
	}
}

func TestParseRequestURIOrPanic_ok(t *testing.T) {
	got := ParseRequestURIOrPanic("https://example.com/api?x=1")
	if got.Host != "example.com" || got.Path != "/api" {
		t.Errorf("parsed wrong: %+v", got)
	}
}

func TestParseRequestURIOrPanic_panicsOnInvalid(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Error("expected panic on invalid URI")
		}
		got := rec.(string)
		if got != "urlutil: ParseRequestURIOrPanic: request URI is invalid" {
			t.Fatalf("panic = %q, want stable invalid-URI message", got)
		}
		if strings.Contains(got, "secret-token") || strings.Contains(got, "%zz") {
			t.Fatalf("panic leaked raw request URI: %q", got)
		}
	}()
	// "%zz" is rejected by ParseRequestURI as an invalid percent-escape.
	_ = ParseRequestURIOrPanic("/callback/%zz?token=secret-token")
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}
