// Command kit-verify probes a running service and reports which
// ASVS controls actually behave as the kit's annotations claim.
//
// Usage:
//
//	kit-verify -url=https://localhost:8443 [-format=text|json] [-soft] [-no-allow-skips]
//
// Exit codes:
//   - 0: every probe is PASS, or SKIPPED unless -no-allow-skips is set.
//     With -soft, UNKNOWN results are also treated as zero.
//   - 1: at least one probe FAILED, or an UNKNOWN probe was not
//     accepted (default; opt out with -soft).
//   - 2: tool error (bad flags, malformed URL).
//
// Probe status semantics — audit FR-005 [MED]:
//
//   - PASS    — the control behaved as claimed.
//   - FAIL    — the control did not behave as claimed (always counts
//     against the run regardless of flags).
//   - SKIPPED — the probe deliberately did not run (e.g. the user has
//     not configured a route the probe needs to hit).
//     Counted as pass unless `-no-allow-skips` is set.
//   - UNKNOWN — the probe ran but the response was inconclusive
//     (e.g. 404 from a route-by-convention probe could mean
//     "JWT not wired" OR "no /api/v1/whoami in this
//     service"). Counted as failure by default; switch to
//     `-soft` to treat as warning.
//
// Pre-fix (audit FR-004 [HIGH]) the default treated everything except
// readiness as soft, so JWT/CSRF/rate-limit failures could still exit
// 0 in CI — a false negative. The new default fails on any FAIL or
// UNKNOWN; -soft restores the older permissive mode for exploratory
// runs.
//
// The probes are intentionally minimal in v2 — verifying every
// possible kit-shipped behaviour would require a full integration
// test framework. Each probe SHOULD link to at least one ASVS ID via
// [Probe.Controls] so kit-verify output ties back to the kit-doctor
// scanner's catalog.
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
	"github.com/bds421/rho-kit/security/v2/asvs"
)

const usage = "usage: kit-verify -url=URL [-format=text|json] [-soft] [-no-allow-skips] [-insecure] [-timeout-ms=N] [-readiness-path=/ready] [-headers-path=/] [-jwt-path=/api/v1/whoami] [-csrf-path=/api/v1/state] [-ratelimit-path=/]"

var errRedirectBlocked = errors.New("kit-verify: redirects are disabled")

// errFlagParseReported marks a flag-parse failure that the flag package
// has already written to stderr (error message plus usage). run() must
// not re-print it, otherwise every bad-flag invocation emits the same
// message twice. Post-parse validation errors are NOT wrapped in this,
// so they are still printed exactly once by run().
var errFlagParseReported = errors.New("kit-verify: flag parse error already reported")

type config struct {
	target       string
	format       string
	soft         bool
	noAllowSkips bool
	insecureTLS  bool
	timeoutMS    int

	readinessPath string
	headersPath   string
	jwtPath       string
	csrfPath      string
	rateLimitPath string
}

type probeConfig struct {
	insecureTLS bool

	base          *url.URL
	readinessPath string
	headersPath   string
	jwtPath       string
	csrfPath      string
	rateLimitPath string
}

// Status enumerates the four probe outcomes. See package doc for
// semantics. The string values match the JSON output and `[STATUS]`
// markers printed in text mode, so downstream pipelines can grep or
// parse them stably.
type Status string

const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusSkipped Status = "SKIPPED"
	StatusUnknown Status = "UNKNOWN"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		// The flag package already wrote flag-parse errors (and usage)
		// to stderr; re-printing here would duplicate the message. Only
		// post-parse validation errors still need to be printed.
		if !errors.Is(err, errFlagParseReported) {
			_, _ = fmt.Fprintln(stderr, err)
		}
		return 2
	}

	base, err := url.Parse(cfg.target)
	if err != nil || base.Scheme == "" || base.Host == "" {
		_, _ = fmt.Fprintln(stderr, "kit-verify: -url must be an absolute URL")
		return 2
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		_, _ = fmt.Fprintln(stderr, "kit-verify: -url scheme must be http or https")
		return 2
	}
	probeCfg := probeConfig{
		insecureTLS:   cfg.insecureTLS,
		base:          base,
		readinessPath: cfg.readinessPath,
		headersPath:   cfg.headersPath,
		jwtPath:       cfg.jwtPath,
		csrfPath:      cfg.csrfPath,
		rateLimitPath: cfg.rateLimitPath,
	}

	hc := newProbeHTTPClient(time.Duration(cfg.timeoutMS)*time.Millisecond, cfg.insecureTLS)

	results := runAllWithConfig(hc, probeCfg)

	if cfg.format == "json" {
		if err := json.NewEncoder(stdout).Encode(results); err != nil {
			_, _ = fmt.Fprintf(stderr, "kit-verify: encode results: %v\n", err)
			return 1
		}
	} else {
		printResults(stdout, results)
	}

	if exitNonZero(results, cfg.soft, cfg.noAllowSkips) {
		return 1
	}
	return 0
}

func newProbeHTTPClient(timeout time.Duration, insecureTLS bool) *http.Client {
	transport := cloneDefaultTransport()
	tlsConfig := cloneTLSConfigWithFloor(transport.TLSClientConfig)
	tlsConfig.InsecureSkipVerify = insecureTLS //nolint:gosec // explicit dev-only flag
	transport.TLSClientConfig = tlsConfig
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: blockRedirect,
	}
}

func cloneDefaultTransport() *http.Transport {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		return tr.Clone()
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func cloneTLSConfigWithFloor(cfg *tls.Config) *tls.Config {
	cloned, err := tlsclone.ConfigOrEmptyWithFloor(cfg, tls.VersionTLS12)
	if err != nil {
		panic("kit-verify: default HTTP client TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned
}

func blockRedirect(_ *http.Request, _ []*http.Request) error {
	return errRedirectBlocked
}

func parseConfig(args []string, stderr io.Writer) (config, error) {
	cfg := config{}
	fs := flag.NewFlagSet("kit-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.target, "url", "", "base URL of the running service (required)")
	fs.StringVar(&cfg.format, "format", "text", "output format: text|json")
	fs.BoolVar(&cfg.soft, "soft", false, "treat UNKNOWN results as pass (exploratory mode); FAIL still exits 1")
	fs.BoolVar(&cfg.noAllowSkips, "no-allow-skips", false, "treat SKIPPED probes as failures (route-by-convention probes must have an endpoint configured)")
	fs.BoolVar(&cfg.insecureTLS, "insecure", false, "skip TLS cert verification (dev only)")
	fs.IntVar(&cfg.timeoutMS, "timeout-ms", 5000, "per-request HTTP timeout in milliseconds (not wall-clock per multi-request probe)")
	fs.StringVar(&cfg.readinessPath, "readiness-path", "/ready", "readiness probe path")
	fs.StringVar(&cfg.headersPath, "headers-path", "/", "path used for security-header, request-id, and correlation-id probes")
	fs.StringVar(&cfg.jwtPath, "jwt-path", "/api/v1/whoami", "JWT-gated probe path")
	fs.StringVar(&cfg.csrfPath, "csrf-path", "/api/v1/state", "state-changing CSRF probe path")
	fs.StringVar(&cfg.rateLimitPath, "ratelimit-path", "/", "rate-limit probe path")
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError + fs.SetOutput(stderr) means the flag
		// package has already written the error and usage to stderr.
		// Preserve flag.ErrHelp (run() exits 0 on -h/-help); wrap any
		// other parse error so run() does not duplicate the message.
		if errors.Is(err, flag.ErrHelp) {
			return cfg, err
		}
		return cfg, fmt.Errorf("%w: %w", errFlagParseReported, err)
	}
	if cfg.target == "" {
		return cfg, errors.New(usage)
	}
	switch cfg.format {
	case "text", "json":
	default:
		return cfg, fmt.Errorf("kit-verify: -format must be text or json")
	}
	if cfg.timeoutMS <= 0 {
		return cfg, fmt.Errorf("kit-verify: -timeout-ms must be positive")
	}
	for _, entry := range []struct {
		name  string
		value string
	}{
		{"-readiness-path", cfg.readinessPath},
		{"-headers-path", cfg.headersPath},
		{"-jwt-path", cfg.jwtPath},
		{"-csrf-path", cfg.csrfPath},
		{"-ratelimit-path", cfg.rateLimitPath},
	} {
		if err := validateProbePath(entry.name, entry.value); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func validateProbePath(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("kit-verify: %s must be a valid origin-form path", name)
	}
	if parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return fmt.Errorf("kit-verify: %s must be an origin-form path starting with / and may include a query string", name)
	}
	return nil
}

// Result captures a single probe's outcome.
type Result struct {
	Probe    string   `json:"probe"`
	Controls []string `json:"controls"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
}

// Probe describes a kit-verify check. Run returns the outcome
// status and a human-readable detail. Use one of [pass]/[fail]/
// [unknown] helpers to build the return value so the four-state
// invariant is clear in every probe.
type Probe struct {
	Name     string
	Controls []asvs.ID
	Run      func(*http.Client, probeConfig) (Status, string)
}

func runAllWithConfig(hc *http.Client, cfg probeConfig) []Result {
	out := make([]Result, 0, len(probes)+1)
	if cfg.insecureTLS {
		// Surface that this run is not an authoritative compliance signal
		// so CI logs cannot silently treat -insecure as a real attest (review-24).
		out = append(out, Result{
			Probe:  "tls-verification",
			Status: StatusUnknown,
			Detail: "certificate verification disabled via -insecure; results are not authoritative for compliance",
		})
	}
	for _, p := range probes {
		status, detail := p.Run(hc, cfg)
		ids := make([]string, len(p.Controls))
		for i, id := range p.Controls {
			ids[i] = string(id)
		}
		out = append(out, Result{
			Probe:    p.Name,
			Controls: ids,
			Status:   status,
			Detail:   detail,
		})
	}
	return out
}

func printResults(w io.Writer, results []Result) {
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "[%s] %s — %v\n", r.Status, r.Probe, r.Controls)
		if r.Detail != "" {
			_, _ = fmt.Fprintf(w, "       %s\n", r.Detail)
		}
	}
}

// exitNonZero implements the FR-004 default-fails policy. FAIL is
// always non-zero. UNKNOWN is non-zero unless -soft. SKIPPED is
// non-zero only when -no-allow-skips. PASS is always zero.
func exitNonZero(results []Result, soft, noAllowSkips bool) bool {
	for _, r := range results {
		switch r.Status {
		case StatusFail:
			return true
		case StatusUnknown:
			if !soft {
				return true
			}
		case StatusSkipped:
			if noAllowSkips {
				return true
			}
		}
	}
	return false
}

// pass / fail / unknown are tiny helpers that make every
// probe's return statement explicitly name the four-state
// invariant. They reduce the chance of accidentally treating an
// inconclusive 404 as a pass.
func pass(detail string) (Status, string)    { return StatusPass, detail }
func fail(detail string) (Status, string)    { return StatusFail, detail }
func unknown(detail string) (Status, string) { return StatusUnknown, detail }

func (cfg probeConfig) url(probePath string) string {
	u := *cfg.base
	parsed, _ := url.Parse(probePath)
	u.Path = pathpkg.Join(cfg.base.Path, parsed.Path)
	if strings.HasSuffix(parsed.Path, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	u.RawQuery = parsed.RawQuery
	u.Fragment = ""
	return u.String()
}

// probes is the kit's verification catalog. New probes plug in here.
//
// Probe naming convention: <package-or-feature>-<expected-behaviour>.
// Each probe SHOULD link to at least one ASVS ID via Controls so
// kit-verify output ties back to the kit-doctor scanner's catalog.
var probes = []Probe{
	{
		Name:     "readiness-200",
		Controls: []asvs.ID{"V14.1.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			return expectStatus(hc, cfg.url(cfg.readinessPath), http.StatusOK)
		},
	},
	{
		Name:     "readiness-no-store",
		Controls: []asvs.ID{"V8.2.2"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			return expectHeader(hc, cfg.url(cfg.readinessPath), "Cache-Control", "no-store")
		},
	},
	{
		Name:     "secheaders-x-content-type-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			return expectHeader(hc, cfg.url(cfg.headersPath), "X-Content-Type-Options", "nosniff")
		},
	},
	{
		Name:     "secheaders-x-frame-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			return expectHeaderToken(hc, cfg.url(cfg.headersPath), "X-Frame-Options", "DENY", "SAMEORIGIN")
		},
	},
	{
		Name:     "request-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			target := cfg.url(cfg.headersPath)
			req, _ := http.NewRequest(http.MethodGet, target, nil)
			resp, err := hc.Do(req)
			if err != nil {
				return fail("GET request failed")
			}
			defer func() { _ = resp.Body.Close() }()
			if _, ok := singletonResponseHeader(resp, "X-Request-Id"); !ok {
				return fail("response missing X-Request-Id header")
			}
			return pass("")
		},
	},
	{
		Name:     "correlation-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			target := cfg.url(cfg.headersPath)
			req, _ := http.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("X-Correlation-Id", "kit-verify-probe")
			resp, err := hc.Do(req)
			if err != nil {
				return fail("GET request failed")
			}
			defer func() { _ = resp.Body.Close() }()
			got, ok := singletonResponseHeader(resp, "X-Correlation-Id")
			if !ok || got != "kit-verify-probe" {
				return fail("X-Correlation-Id round-trip did not match")
			}
			return pass("")
		},
	},
	{
		Name:     "jwt-rejects-missing-token",
		Controls: []asvs.ID{"V2.1.5", "V3.2.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			// Probes a JWT-gated route by convention. 401/403 → pass.
			// 404 → UNKNOWN: we cannot tell whether JWT is wired
			// correctly or this service simply doesn't expose a
			// /api/v1/whoami route. Anything else → FAIL.
			req, _ := http.NewRequest(http.MethodGet, cfg.url(cfg.jwtPath), nil)
			resp, err := hc.Do(req)
			if err != nil {
				return fail("GET request failed")
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return pass("")
			case http.StatusNotFound:
				return unknown("JWT probe returned 404 — JWT enforcement not exercised; configure -jwt-path or pass -soft to ignore")
			default:
				return fail(fmt.Sprintf("expected 401/403, got %d", resp.StatusCode))
			}
		},
	},
	{
		Name:     "csrf-rejects-state-changing-without-token",
		Controls: []asvs.ID{"V13.2.3"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			// POST without CSRF token MUST be rejected by the kit CSRF
			// middleware with 403. Send JSON so content-type guards do
			// not mask the CSRF result. 404 → UNKNOWN (no
			// state-changing route to probe); 401 means auth likely ran
			// first, so CSRF enforcement was not proved.
			req, _ := http.NewRequest(http.MethodPost, cfg.url(cfg.csrfPath), strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			resp, err := hc.Do(req)
			if err != nil {
				return fail("POST request failed")
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusForbidden:
				return pass("")
			case http.StatusNotFound:
				return unknown("CSRF probe returned 404 — CSRF enforcement not exercised; configure -csrf-path or pass -soft to ignore")
			case http.StatusUnauthorized:
				return fail("got 401; auth rejected the request before kit CSRF returned 403")
			default:
				return fail(fmt.Sprintf("expected 403 CSRF rejection, got %d", resp.StatusCode))
			}
		},
	},
	{
		Name:     "ratelimit-emits-retry-after-on-429",
		Controls: []asvs.ID{"V2.2.1", "V11.1.1"},
		Run: func(hc *http.Client, cfg probeConfig) (Status, string) {
			// Hammer the configured path enough times to trip a
			// default rate limit. No 429 in 30 requests → UNKNOWN
			// (the limit may be above the probe burst, or no rate
			// limiter is wired). 429 with no Retry-After → FAIL
			// (header is required by RFC 9110 §15.5.6 for clients to
			// back off correctly).
			const burst = 30
			saw429 := false
			retryAfter := ""
			target := cfg.url(cfg.rateLimitPath)
			for i := 0; i < burst; i++ {
				resp, err := hc.Get(target)
				if err != nil {
					return fail("GET request failed")
				}
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusTooManyRequests {
					saw429 = true
					retryAfter, _ = singletonResponseHeader(resp, "Retry-After")
					break
				}
			}
			if !saw429 {
				return unknown(fmt.Sprintf("no 429 in %d requests — limit may be above probe burst, or no rate limiter is wired", burst))
			}
			if retryAfter == "" {
				return fail("429 response missing Retry-After header")
			}
			return pass(fmt.Sprintf("429 with Retry-After=%s", retryAfter))
		},
	},
}

func expectStatus(hc *http.Client, url string, want int) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail("GET request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return fail(fmt.Sprintf("GET response status mismatch: want %d, got %d", want, resp.StatusCode))
	}
	return pass("")
}

func expectHeader(hc *http.Client, url, key, contains string) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail("GET request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	got, ok := mergedResponseHeader(resp, key)
	if !ok || (contains != "" && !containsFold(got, contains)) {
		return fail(fmt.Sprintf("response header %s does not contain expected value", key))
	}
	return pass("")
}

func expectHeaderPresent(hc *http.Client, url, key string) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail("GET request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if _, ok := mergedResponseHeader(resp, key); !ok {
		return fail(fmt.Sprintf("response header %s missing", key))
	}
	return pass("")
}

// singletonResponseHeader returns the value of key only when the
// response carries exactly one non-empty field line for it. It is used
// for identity/echo headers (X-Request-Id, X-Correlation-Id) and
// Retry-After, where more than one field line would itself indicate a
// middleware bug, so collapsing duplicates would mask the defect.
func singletonResponseHeader(resp *http.Response, key string) (string, bool) {
	values := resp.Header.Values(key)
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", false
	}
	return value, true
}

// mergedResponseHeader returns the comma-joined value of all non-empty
// field lines for key, treating an absent header (or one whose lines are
// all blank) as not present. Per RFC 9110 §5.2/§5.3 multiple field lines
// with the same name are semantically equivalent to a single value with
// the lines comma-joined in order; a proxy appending its own
// Cache-Control or X-Frame-Options line is legal. The value-presence
// header probes therefore must not treat a legitimate duplicate as a
// missing/mismatched header.
func mergedResponseHeader(resp *http.Response, key string) (string, bool) {
	values := resp.Header.Values(key)
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ", "), true
}

// containsFold reports whether s contains substr as a whole directive
// token (split on commas/semicolons), case-insensitively. Used by header
// probes so "nosniff" does not match a fictional "x-nosniff-off" value and
// "no-store" still matches inside "private, no-store".
func containsFold(s, substr string) bool {
	want := strings.ToLower(strings.TrimSpace(substr))
	if want == "" {
		return true
	}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';'
	}) {
		if strings.ToLower(strings.TrimSpace(part)) == want {
			return true
		}
	}
	return false
}

// expectHeaderToken GETs url and requires header key to contain at least
// one of the allowed tokens (case-insensitive whole-token match).
func expectHeaderToken(hc *http.Client, url, key string, allowed ...string) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail("GET request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	got, ok := mergedResponseHeader(resp, key)
	if !ok {
		return fail(fmt.Sprintf("response header %s missing", key))
	}
	for _, a := range allowed {
		if containsFold(got, a) {
			return pass("")
		}
	}
	return fail(fmt.Sprintf("response header %s value %q is not one of %v", key, got, allowed))
}
