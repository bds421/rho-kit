package httpx

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeRedirect_AllowsRelativeTargets(t *testing.T) {
	tests := map[string]string{
		"absolute path": "/dashboard?tab=billing#top",
		"relative path": "settings/profile",
		"query only":    "?next=1",
		"fragment only": "#done",
	}

	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := SafeRedirect(target)
			if err != nil {
				t.Fatalf("SafeRedirect(%q): %v", target, err)
			}
			if got != target {
				t.Fatalf("SafeRedirect(%q) = %q, want %q", target, got, target)
			}
		})
	}
}

func TestSafeRedirect_AllowsConfiguredAbsoluteHosts(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		allowed []string
		want    string
	}{
		{
			name:    "https host",
			target:  "https://accounts.example.com/oauth/callback?code=abc",
			allowed: []string{"accounts.example.com"},
			want:    "https://accounts.example.com/oauth/callback?code=abc",
		},
		{
			name:    "case insensitive host",
			target:  "https://Accounts.Example.Com/welcome",
			allowed: []string{"accounts.example.com"},
			want:    "https://Accounts.Example.Com/welcome",
		},
		{
			name:    "explicit port",
			target:  "http://localhost:8080/dev",
			allowed: []string{"localhost:8080"},
			want:    "http://localhost:8080/dev",
		},
		{
			name:    "trailing dot allowed host",
			target:  "https://example.com./done",
			allowed: []string{"example.com"},
			want:    "https://example.com./done",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SafeRedirect(tt.target, tt.allowed...)
			if err != nil {
				t.Fatalf("SafeRedirect(%q): %v", tt.target, err)
			}
			if got != tt.want {
				t.Fatalf("SafeRedirect(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestSafeRedirect_RejectsUnsafeTargets(t *testing.T) {
	tests := map[string]string{
		"empty":                    "",
		"surrounding whitespace":   " /dashboard",
		"scheme relative":          "//evil.example/phish",
		"encoded scheme relative":  "/%2f%2fevil.example/phish",
		"encoded backslash host":   "/%5c%5cevil.example/phish",
		"mixed encoded separator":  "/%2f%5cevil.example/phish",
		"backslash":                "\\\\evil.example\\phish",
		"external absolute":        "https://evil.example/phish",
		"empty explicit port":      "https://accounts.example.com:/phish",
		"zero explicit port":       "https://accounts.example.com:0/phish",
		"too large explicit port":  "https://accounts.example.com:65536/phish",
		"zone identifier":          "https://[fe80::1%25lo0]/phish",
		"wrong explicit port":      "https://accounts.example.com:444/phish",
		"javascript scheme":        "javascript:alert(1)",
		"absolute without host":    "https:evil.example",
		"userinfo":                 "https://user@accounts.example.com/path",
		"malformed percent escape": "%zz",
		"newline":                  "/dashboard\nLocation: https://evil.example",
		"internal whitespace":      "/dashboard menu",
		"invalid utf8":             string([]byte{'/', 'd', 'a', 's', 'h', 0xff}),
	}

	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := SafeRedirect(target, "accounts.example.com:443")
			if !errors.Is(err, ErrUnsafeRedirect) {
				t.Fatalf("SafeRedirect(%q) error = %v, want ErrUnsafeRedirect", target, err)
			}
		})
	}
}

func TestSafeRedirect_ParseErrorDoesNotEchoTarget(t *testing.T) {
	_, err := SafeRedirect("/return/%zz?token=secret-token")
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("SafeRedirect error = %v, want ErrUnsafeRedirect", err)
	}
	if !strings.Contains(err.Error(), "target URL is invalid") {
		t.Fatalf("SafeRedirect error = %v, want invalid URL marker", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "token=") || strings.Contains(err.Error(), "%zz") {
		t.Fatalf("SafeRedirect leaked target value: %v", err)
	}
}

func TestSafeRedirect_RejectionsDoNotEchoSchemeOrHost(t *testing.T) {
	tests := map[string]string{
		"scheme":       "secret-token-scheme:alert(1)",
		"host":         "https://secret-token.example/phish",
		"invalid host": "https://secret-token.example:bad/phish",
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := SafeRedirect(target, "accounts.example.com")
			if !errors.Is(err, ErrUnsafeRedirect) {
				t.Fatalf("SafeRedirect error = %v, want ErrUnsafeRedirect", err)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("SafeRedirect leaked target component: %v", err)
			}
		})
	}
}

func TestSafeRedirect_PortlessAllowedHostRejectsExplicitPort(t *testing.T) {
	_, err := SafeRedirect("https://accounts.example.com:8443/dev", "accounts.example.com")
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("SafeRedirect error = %v, want ErrUnsafeRedirect", err)
	}
}
