// Command kit-verify probes a running service and reports which
// ASVS controls actually behave as the kit's annotations claim.
//
// Usage:
//
//	kit-verify -url=https://localhost:8443 [-format=text|json] [-soft] [-allow-skips]
//
// Exit codes:
//   - 0: every probe is PASS or (with -allow-skips) SKIPPED. With -soft,
//     UNKNOWN results are also treated as zero.
//   - 1: at least one probe FAILED, or an UNKNOWN probe was not
//     accepted (default; opt out with -soft).
//   - 2: tool error (target unreachable, malformed URL).
//
// Probe status semantics — audit FR-005 [MED]:
//
//   - PASS    — the control behaved as claimed.
//   - FAIL    — the control did not behave as claimed (always counts
//               against the run regardless of flags).
//   - SKIPPED — the probe deliberately did not run (e.g. the user has
//               not configured a route the probe needs to hit).
//               Counted as pass unless `-no-allow-skips` is set.
//   - UNKNOWN — the probe ran but the response was inconclusive
//               (e.g. 404 from a route-by-convention probe could mean
//               "JWT not wired" OR "no /api/v1/whoami in this
//               service"). Counted as failure by default; switch to
//               `-soft` to treat as warning.
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
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/bds421/rho-kit/security/v2/asvs"
)

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
	target := flag.String("url", "", "base URL of the running service (required)")
	format := flag.String("format", "text", "output format: text|json")
	soft := flag.Bool("soft", false, "treat UNKNOWN results as pass (exploratory mode); FAIL still exits 1")
	noAllowSkips := flag.Bool("no-allow-skips", false, "treat SKIPPED probes as failures (route-by-convention probes must have an endpoint configured)")
	insecureTLS := flag.Bool("insecure", false, "skip TLS cert verification (dev only)")
	timeoutMS := flag.Int("timeout-ms", 5000, "per-probe timeout in milliseconds")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "usage: kit-verify -url=URL [-format=...] [-soft] [-no-allow-skips] [-insecure] [-timeout-ms=N]")
		os.Exit(2)
	}
	base, err := url.Parse(*target)
	if err != nil || base.Scheme == "" || base.Host == "" {
		fmt.Fprintf(os.Stderr, "kit-verify: -url must be an absolute URL (got %q)\n", *target)
		os.Exit(2)
	}

	hc := &http.Client{
		Timeout: time.Duration(*timeoutMS) * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: *insecureTLS, //nolint:gosec // explicit dev-only flag
				MinVersion:         tls.VersionTLS12,
			},
		},
	}

	results := runAll(hc, base.String())

	if *format == "json" {
		_ = json.NewEncoder(os.Stdout).Encode(results)
	} else {
		printResults(results)
	}

	if exitNonZero(results, *soft, *noAllowSkips) {
		os.Exit(1)
	}
}

// Result captures a single probe's outcome.
type Result struct {
	Probe    string   `json:"probe"`
	Controls []string `json:"controls"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
}

// Probe describes a kit-verify check. Run returns the outcome
// status and a human-readable detail. Use one of [pass]/[fail]/[skip]/
// [unknown] helpers to build the return value so the four-state
// invariant is clear in every probe.
type Probe struct {
	Name     string
	Controls []asvs.ID
	Run      func(*http.Client, string) (Status, string)
}

func runAll(hc *http.Client, base string) []Result {
	out := make([]Result, 0, len(probes))
	for _, p := range probes {
		status, detail := p.Run(hc, base)
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

func printResults(results []Result) {
	for _, r := range results {
		fmt.Printf("[%s] %s — %v\n", r.Status, r.Probe, r.Controls)
		if r.Detail != "" {
			fmt.Printf("       %s\n", r.Detail)
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

// pass / fail / skip / unknown are tiny helpers that make every
// probe's return statement explicitly name the four-state
// invariant. They reduce the chance of accidentally treating an
// inconclusive 404 as a pass.
func pass(detail string) (Status, string)    { return StatusPass, detail }
func fail(detail string) (Status, string)    { return StatusFail, detail }
func skip(detail string) (Status, string)    { return StatusSkipped, detail }
func unknown(detail string) (Status, string) { return StatusUnknown, detail }

// probes is the kit's verification catalog. New probes plug in here.
//
// Probe naming convention: <package-or-feature>-<expected-behaviour>.
// Each probe SHOULD link to at least one ASVS ID via Controls so
// kit-verify output ties back to the kit-doctor scanner's catalog.
var probes = []Probe{
	{
		Name:     "readiness-200",
		Controls: []asvs.ID{"V14.1.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			return expectStatus(hc, base+"/ready", http.StatusOK)
		},
	},
	{
		Name:     "readiness-no-store",
		Controls: []asvs.ID{"V8.2.2"},
		Run: func(hc *http.Client, base string) (Status, string) {
			return expectHeader(hc, base+"/ready", "Cache-Control", "no-store")
		},
	},
	{
		Name:     "secheaders-x-content-type-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			return expectHeader(hc, base+"/", "X-Content-Type-Options", "nosniff")
		},
	},
	{
		Name:     "secheaders-x-frame-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			return expectHeaderPresent(hc, base+"/", "X-Frame-Options")
		},
	},
	{
		Name:     "request-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return fail(fmt.Sprintf("GET %s/: %v", base, err))
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.Header.Get("X-Request-Id") == "" {
				return fail("response missing X-Request-Id header")
			}
			return pass("")
		},
	},
	{
		Name:     "correlation-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
			req.Header.Set("X-Correlation-Id", "kit-verify-probe")
			resp, err := hc.Do(req)
			if err != nil {
				return fail(fmt.Sprintf("GET %s/: %v", base, err))
			}
			defer func() { _ = resp.Body.Close() }()
			got := resp.Header.Get("X-Correlation-Id")
			if got != "kit-verify-probe" {
				return fail(fmt.Sprintf("X-Correlation-Id round-trip = %q, want %q", got, "kit-verify-probe"))
			}
			return pass("")
		},
	},
	{
		Name:     "jwt-rejects-missing-token",
		Controls: []asvs.ID{"V2.1.5", "V3.2.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			// Probes a JWT-gated route by convention. 401/403 → pass.
			// 404 → UNKNOWN: we cannot tell whether JWT is wired
			// correctly or this service simply doesn't expose a
			// /api/v1/whoami route. Anything else → FAIL.
			req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/whoami", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return fail(fmt.Sprintf("GET %s: %v", req.URL.String(), err))
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return pass("")
			case http.StatusNotFound:
				return unknown("no /api/v1/whoami route — JWT enforcement not exercised; configure a JWT-gated probe path or pass -soft to ignore")
			default:
				return fail(fmt.Sprintf("expected 401/403, got %d", resp.StatusCode))
			}
		},
	},
	{
		Name:     "csrf-rejects-state-changing-without-token",
		Controls: []asvs.ID{"V13.2.3"},
		Run: func(hc *http.Client, base string) (Status, string) {
			// POST without CSRF token MUST be rejected. 4xx → pass;
			// 404 → UNKNOWN (no state-changing route to probe);
			// 2xx/5xx → FAIL.
			req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/state", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return fail(fmt.Sprintf("POST %s: %v", req.URL.String(), err))
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusForbidden, http.StatusBadRequest, http.StatusUnauthorized:
				return pass("")
			case http.StatusNotFound:
				return unknown("no /api/v1/state route — CSRF enforcement not exercised; configure a state-changing probe path or pass -soft to ignore")
			default:
				return fail(fmt.Sprintf("expected 4xx rejection, got %d", resp.StatusCode))
			}
		},
	},
	{
		Name:     "ratelimit-emits-retry-after-on-429",
		Controls: []asvs.ID{"V2.2.1", "V11.1.1"},
		Run: func(hc *http.Client, base string) (Status, string) {
			// Hammer / enough times to trip a default rate limit. No
			// 429 in 30 requests → UNKNOWN (the limit may be above
			// the probe burst, or no rate limiter is wired). 429 with
			// no Retry-After → FAIL (header is required by RFC 9110
			// §15.5.6 for clients to back off correctly).
			const burst = 30
			saw429 := false
			retryAfter := ""
			for i := 0; i < burst; i++ {
				resp, err := hc.Get(base + "/")
				if err != nil {
					return fail(fmt.Sprintf("GET %s/: %v", base, err))
				}
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusTooManyRequests {
					saw429 = true
					retryAfter = resp.Header.Get("Retry-After")
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
		return fail(fmt.Sprintf("GET %s: %v", url, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return fail(fmt.Sprintf("GET %s: want status %d, got %d", url, want, resp.StatusCode))
	}
	return pass("")
}

func expectHeader(hc *http.Client, url, key, contains string) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail(fmt.Sprintf("GET %s: %v", url, err))
	}
	defer func() { _ = resp.Body.Close() }()
	got := resp.Header.Get(key)
	if got == "" || (contains != "" && !containsFold(got, contains)) {
		return fail(fmt.Sprintf("GET %s: header %s=%q does not contain %q", url, key, got, contains))
	}
	return pass("")
}

func expectHeaderPresent(hc *http.Client, url, key string) (Status, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return fail(fmt.Sprintf("GET %s: %v", url, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get(key) == "" {
		return fail(fmt.Sprintf("GET %s: header %s missing", url, key))
	}
	return pass("")
}

// containsFold reports whether s contains substr, case-insensitively.
// Used by header probes where servers may emit headers in any case
// (per RFC 9110 §5.1, header names are case-insensitive but values
// often need flexible matching too — e.g., "no-store" vs "No-Store").
func containsFold(s, substr string) bool {
	if substr == "" {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
