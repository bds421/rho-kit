package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProbeHTTPClient_BlocksRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := newProbeHTTPClient(2*time.Second, false).Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want errRedirectBlocked", err)
	}
}

func TestNewProbeHTTPClient_TLSConfig(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	base := http.DefaultTransport.(*http.Transport).Clone()
	callerTLSConfig := &tls.Config{
		MinVersion: tls.VersionTLS10,
		ServerName: "probe.internal.test",
		NextProtos: []string{"h2"},
	}
	base.TLSClientConfig = callerTLSConfig
	http.DefaultTransport = base

	client := newProbeHTTPClient(2*time.Second, true)
	callerTLSConfig.NextProtos[0] = "mutated"

	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "transport type = %T", client.Transport)
	require.NotNil(t, tr.TLSClientConfig)
	require.NotSame(t, callerTLSConfig, tr.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), tr.TLSClientConfig.MinVersion)
	assert.Equal(t, uint16(tls.VersionTLS10), callerTLSConfig.MinVersion)
	assert.True(t, tr.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, "probe.internal.test", tr.TLSClientConfig.ServerName)
	require.NotEmpty(t, tr.TLSClientConfig.NextProtos)
	assert.Equal(t, "h2", tr.TLSClientConfig.NextProtos[0])
}

func TestNewProbeHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = probeRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	client := newProbeHTTPClient(2*time.Second, false)
	if _, ok := client.Transport.(*http.Transport); !ok {
		t.Fatalf("transport = %T, want *http.Transport fallback", client.Transport)
	}
}

func TestNewProbeHTTPClient_PanicsWhenTLSMaxVersionBelowFloor(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MaxVersion: tls.VersionTLS11}
	http.DefaultTransport = base

	require.PanicsWithValue(t, "kit-verify: default HTTP client TLS MaxVersion must allow TLS 1.2 or newer", func() {
		newProbeHTTPClient(2*time.Second, false)
	})
}

type probeRoundTripper func(*http.Request) (*http.Response, error)

func (f probeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunAll_AllPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		// Simulate request-id + correlation-id middleware:
		// generate an X-Request-Id, echo X-Correlation-Id.
		w.Header().Set("X-Request-Id", "test-rid-1")
		if cid := r.Header.Get("X-Correlation-Id"); cid != "" {
			w.Header().Set("X-Correlation-Id", cid)
		}
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/whoami":
			// JWT-gated route returns 401 without auth header.
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/v1/state":
			// CSRF-gated route returns 403 without token.
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)
	for _, r := range results {
		// Allow PASS for everything except the rate-limit probe (this
		// fixture doesn't 429), which legitimately surfaces as UNKNOWN.
		if r.Probe == "ratelimit-emits-retry-after-on-429" {
			assert.Equalf(t, StatusUnknown, r.Status, "probe %s should be UNKNOWN without an active rate limiter: %s", r.Probe, r.Detail)
			continue
		}
		assert.Equalf(t, StatusPass, r.Status, "probe %s status=%s detail=%s", r.Probe, r.Status, r.Detail)
	}
}

func TestRunAll_DetectsMissingHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately omit X-Content-Type-Options to fail that probe.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)

	for _, r := range results {
		if r.Probe == "secheaders-x-content-type-options" {
			assert.Equalf(t, StatusFail, r.Status, "missing X-Content-Type-Options must FAIL: %s", r.Detail)
			return
		}
	}
	t.Fatal("secheaders-x-content-type-options probe not found in results")
}

func TestRunAll_DuplicateListHeadersPass(t *testing.T) {
	// RFC 9110 §5.2/§5.3: multiple field lines with the same name are
	// equivalent to a single comma-joined value. A proxy appending its
	// own Cache-Control / X-Frame-Options line is legal, so the value
	// and presence probes must NOT treat duplicates as missing/mismatched.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Cache-Control", "no-store")
		w.Header().Add("Cache-Control", "no-store")
		w.Header().Add("X-Content-Type-Options", "nosniff")
		w.Header().Add("X-Content-Type-Options", "nosniff")
		w.Header().Add("X-Frame-Options", "DENY")
		w.Header().Add("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)

	wantPass := map[string]bool{
		"readiness-no-store":                true,
		"secheaders-x-content-type-options": true,
		"secheaders-x-frame-options":        true,
	}
	seen := map[string]bool{}
	for _, r := range results {
		if wantPass[r.Probe] {
			seen[r.Probe] = true
			assert.Equalf(t, StatusPass, r.Status,
				"duplicate-but-valid header must PASS for %s: %s", r.Probe, r.Detail)
		}
	}
	assert.Len(t, seen, len(wantPass), "expected all list-header probes present in results")
}

func TestExpectHeader_AbsentHeaderFails(t *testing.T) {
	// Guard the merge path: a header with no field lines must still FAIL
	// (the merge must not turn "absent" into "present").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	status, detail := expectHeader(&http.Client{Timeout: 2 * time.Second}, srv.URL, "Cache-Control", "no-store")
	assert.Equal(t, StatusFail, status)
	assert.Contains(t, detail, "Cache-Control")

	status, _ = expectHeaderPresent(&http.Client{Timeout: 2 * time.Second}, srv.URL, "X-Frame-Options")
	assert.Equal(t, StatusFail, status)
}

func TestExpectHeader_DuplicateWithOneMatchingValuePasses(t *testing.T) {
	// A proxy may append a different value; the merged value still
	// contains the expected token, so the contains-check must PASS.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Cache-Control", "private")
		w.Header().Add("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	status, detail := expectHeader(&http.Client{Timeout: 2 * time.Second}, srv.URL, "Cache-Control", "no-store")
	assert.Equalf(t, StatusPass, status, "merged Cache-Control should contain no-store: %s", detail)
}

func TestExitNonZero_FailAlwaysExitsNonZero(t *testing.T) {
	// FR-004: a FAIL result must exit non-zero regardless of -soft
	// or -no-allow-skips. The pre-fix code only treated readiness as
	// hard, so JWT/CSRF/rate-limit FAIL results silently passed CI.
	results := []Result{{Probe: "secheaders-x-frame-options", Status: StatusFail}}
	assert.True(t, exitNonZero(results, false, false), "FAIL must fail by default")
	assert.True(t, exitNonZero(results, true, false), "FAIL must still fail under -soft")
	assert.True(t, exitNonZero(results, true, true), "FAIL must still fail under -soft -no-allow-skips")
}

func TestExitNonZero_UnknownFailsByDefault_PassesUnderSoft(t *testing.T) {
	// FR-005: UNKNOWN was previously encoded as "passing" (route 404
	// → success). It now defaults to non-zero unless -soft.
	results := []Result{{Probe: "jwt-rejects-missing-token", Status: StatusUnknown}}
	assert.True(t, exitNonZero(results, false, false), "UNKNOWN must fail by default")
	assert.False(t, exitNonZero(results, true, false), "UNKNOWN must pass under -soft")
}

func TestExitNonZero_SkippedPassesByDefault_FailsUnderNoAllowSkips(t *testing.T) {
	results := []Result{{Probe: "ratelimit-emits-retry-after-on-429", Status: StatusSkipped}}
	assert.False(t, exitNonZero(results, false, false), "SKIPPED must pass by default")
	assert.True(t, exitNonZero(results, false, true), "SKIPPED must fail under -no-allow-skips")
}

func TestExitNonZero_PassNeverFails(t *testing.T) {
	results := []Result{{Probe: "readiness-200", Status: StatusPass}}
	for _, soft := range []bool{false, true} {
		for _, noAllowSkips := range []bool{false, true} {
			assert.Falsef(t, exitNonZero(results, soft, noAllowSkips),
				"PASS must always succeed (soft=%v, noAllowSkips=%v)", soft, noAllowSkips)
		}
	}
}

func TestContainsFold(t *testing.T) {
	assert.True(t, containsFold("Cache-Control: no-store", "no-store"))
	assert.True(t, containsFold("nosniff", "NOSNIFF"))
	assert.False(t, containsFold("DENY", "ALLOW"))
	assert.True(t, containsFold("anything", ""))
}

func TestJWTProbe_404IsUnknown(t *testing.T) {
	// Route-by-convention: a 404 doesn't tell us whether JWT is wired
	// or this service simply doesn't expose /api/v1/whoami. Pre-fix
	// this returned a soft pass; new behaviour is UNKNOWN so CI sees
	// the inconclusive result and either treats it as failure (default)
	// or warning (-soft).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)
	var found bool
	for _, r := range results {
		if r.Probe == "jwt-rejects-missing-token" {
			require.Equal(t, StatusUnknown, r.Status, "404 on JWT probe must be UNKNOWN, got %s (%s)", r.Status, r.Detail)
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestRunAllWithConfig_UsesCustomProbePaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/svc/health":
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
		case "/svc/public":
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-Request-Id", "test-rid-1")
			if cid := r.Header.Get("X-Correlation-Id"); cid != "" {
				w.Header().Set("X-Correlation-Id", cid)
			}
			w.WriteHeader(http.StatusOK)
		case "/svc/auth/me":
			w.WriteHeader(http.StatusUnauthorized)
		case "/svc/state/change":
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		case "/svc/limited":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	base, err := url.Parse(srv.URL + "/svc")
	require.NoError(t, err)

	results := runAllWithConfig(&http.Client{Timeout: 2 * time.Second}, probeConfig{
		base:          base,
		readinessPath: "/health",
		headersPath:   "/public",
		jwtPath:       "/auth/me",
		csrfPath:      "/state/change",
		rateLimitPath: "/limited",
	})

	for _, r := range results {
		assert.Equalf(t, StatusPass, r.Status, "probe %s status=%s detail=%s", r.Probe, r.Status, r.Detail)
	}
}

func TestProbeConfigURL_JoinsBasePathAndProbeQuery(t *testing.T) {
	base, err := url.Parse("https://example.test/service/")
	require.NoError(t, err)
	cfg := probeConfig{base: base}

	assert.Equal(t, "https://example.test/service/ready?deep=1", cfg.url("/ready?deep=1"))
	assert.Equal(t, "https://example.test/service/", cfg.url("/"))
}

func TestParseConfig_RejectsInvalidFormatTimeoutAndProbePath(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      string
		forbidden string
	}{
		{
			name:      "format",
			args:      []string{"-url=https://example.test", "-format=yaml"},
			want:      "-format must be text or json",
			forbidden: "yaml",
		},
		{
			name:      "timeout",
			args:      []string{"-url=https://example.test", "-timeout-ms=0"},
			want:      "-timeout-ms must be positive",
			forbidden: "0",
		},
		{
			name:      "path",
			args:      []string{"-url=https://example.test", "-jwt-path=https://attacker.test/whoami"},
			want:      "-jwt-path must be an origin-form path",
			forbidden: "attacker.test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := parseConfig(tc.args, &stderr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), tc.forbidden)
		})
	}
}

func TestRun_FlagParseErrorPrintedOnce(t *testing.T) {
	// flag.ContinueOnError + fs.SetOutput(stderr) makes the flag package
	// print the error + usage; run() must not re-print the same message,
	// otherwise every bad-flag invocation emits it twice in CI logs.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-nonexistent-flag"}, &stdout, &stderr)

	assert.Equal(t, 2, code)
	out := stderr.String()
	assert.Contains(t, out, "flag provided but not defined: -nonexistent-flag")
	assert.Equal(t, 1, strings.Count(out, "flag provided but not defined: -nonexistent-flag"),
		"flag-parse error must appear exactly once, got:\n%s", out)
	// Usage block from the flag package is still present.
	assert.Contains(t, out, "Usage of kit-verify:")
}

func TestRun_ValidationErrorPrintedOnce(t *testing.T) {
	// Post-parse validation errors are NOT printed by the flag package,
	// so run() is the sole printer and must emit them exactly once.
	var stdout, stderr bytes.Buffer
	code := run([]string{"-url=https://example.test", "-format=yaml"}, &stdout, &stderr)

	assert.Equal(t, 2, code)
	out := stderr.String()
	assert.Equal(t, 1, strings.Count(out, "-format must be text or json"),
		"validation error must appear exactly once, got:\n%s", out)
}

func TestRun_HelpExitsZeroWithoutError(t *testing.T) {
	// -h triggers flag.ErrHelp: run() must exit 0 and not print an error
	// line (the flag package already wrote usage to stderr).
	var stdout, stderr bytes.Buffer
	code := run([]string{"-h"}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	out := stderr.String()
	assert.Contains(t, out, "Usage of kit-verify:")
	assert.NotContains(t, out, "kit-verify: flag parse error already reported")
}

func TestRun_RejectsUnsupportedURLScheme(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-url=ftp://example.test"}, &stdout, &stderr)

	assert.Equal(t, 2, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "-url scheme must be http or https")
	assert.NotContains(t, stderr.String(), "ftp")
}

func TestCSRFProbe_401IsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	base, err := url.Parse(srv.URL)
	require.NoError(t, err)

	var status Status
	var detail string
	for _, p := range probes {
		if p.Name == "csrf-rejects-state-changing-without-token" {
			status, detail = p.Run(&http.Client{Timeout: 2 * time.Second}, probeConfig{
				base:          base,
				readinessPath: "/ready",
				headersPath:   "/",
				jwtPath:       "/api/v1/whoami",
				csrfPath:      "/api/v1/state",
				rateLimitPath: "/",
			})
			break
		}
	}

	require.NotEmpty(t, status, "CSRF probe not found")
	assert.Equal(t, StatusFail, status)
	assert.Contains(t, detail, "auth rejected")
}

func TestProbeFailureDetailsDoNotReflectURLOrHeaderValues(t *testing.T) {
	status, detail := expectHeader(&http.Client{Timeout: 2 * time.Second}, "http://127.0.0.1:1/?token=secret-token", "X-Secret-Header", "secret-value")

	require.Equal(t, StatusFail, status)
	assert.NotContains(t, detail, "127.0.0.1")
	assert.NotContains(t, detail, "secret-token")
	assert.NotContains(t, detail, "secret-value")
}
