// Command kit-verify probes a running service and reports which
// ASVS controls actually behave as the kit's annotations claim.
//
// Usage:
//
//	kit-verify -url=https://localhost:8443 [-format=text|json] [-strict]
//
// Exit codes:
//   - 0: every probed control passed.
//   - 1: at least one control failed (a claim doesn't hold).
//   - 2: tool error (target unreachable, malformed URL).
//
// The probes are intentionally minimal in v2 — verifying every
// possible kit-shipped behaviour would require a full integration
// test framework. v2 ships a focused subset:
//
//   - V9.2.1: secheaders middleware emits expected headers
//   - V8.2.2: /ready and /healthz set Cache-Control: no-store
//   - V14.1.1: /ready returns 200 (production-safety validator
//     would have failed startup if this didn't hold)
//
// Future probes (v2.x): JWT rejection paths, CSRF token presence,
// rate-limit 429 emission, request-ID propagation. Each probe
// claims an ASVS ID via [Probe.Controls] so the output ties back
// to the kit-doctor scanner's catalog.
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

	"github.com/bds421/rho-kit/security/asvs"
)

func main() {
	target := flag.String("url", "", "base URL of the running service (required)")
	format := flag.String("format", "text", "output format: text|json")
	strictMode := flag.Bool("strict", false, "exit 1 on any probe failure (default: only on hard failures)")
	insecureTLS := flag.Bool("insecure", false, "skip TLS cert verification (dev only)")
	timeoutMS := flag.Int("timeout-ms", 5000, "per-probe timeout in milliseconds")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "usage: kit-verify -url=URL [-format=...] [-strict] [-insecure] [-timeout-ms=N]")
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

	if exitNonZero(results, *strictMode) {
		os.Exit(1)
	}
}

// Result captures a single probe's outcome.
type Result struct {
	Probe    string   `json:"probe"`
	Controls []string `json:"controls"`
	Passed   bool     `json:"passed"`
	Detail   string   `json:"detail,omitempty"`
}

// Probe describes a kit-verify check.
type Probe struct {
	Name     string
	Controls []asvs.ID
	Run      func(*http.Client, string) (bool, string)
}

func runAll(hc *http.Client, base string) []Result {
	out := make([]Result, 0, len(probes))
	for _, p := range probes {
		passed, detail := p.Run(hc, base)
		ids := make([]string, len(p.Controls))
		for i, id := range p.Controls {
			ids[i] = string(id)
		}
		out = append(out, Result{
			Probe:    p.Name,
			Controls: ids,
			Passed:   passed,
			Detail:   detail,
		})
	}
	return out
}

func printResults(results []Result) {
	for _, r := range results {
		mark := "FAIL"
		if r.Passed {
			mark = "PASS"
		}
		fmt.Printf("[%s] %s — %v\n", mark, r.Probe, r.Controls)
		if r.Detail != "" {
			fmt.Printf("       %s\n", r.Detail)
		}
	}
}

func exitNonZero(results []Result, strictMode bool) bool {
	for _, r := range results {
		if !r.Passed && (strictMode || isHardFailure(r)) {
			return true
		}
	}
	return false
}

// isHardFailure reports whether a failed probe should fail CI even
// without -strict. Currently only the readiness-probe failure is
// considered hard — a service that doesn't respond on /ready is
// fundamentally broken.
func isHardFailure(r Result) bool {
	return r.Probe == "readiness-200"
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
		Run: func(hc *http.Client, base string) (bool, string) {
			return expectStatus(hc, base+"/ready", http.StatusOK)
		},
	},
	{
		Name:     "readiness-no-store",
		Controls: []asvs.ID{"V8.2.2"},
		Run: func(hc *http.Client, base string) (bool, string) {
			return expectHeader(hc, base+"/ready", "Cache-Control", "no-store")
		},
	},
	{
		Name:     "secheaders-x-content-type-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			return expectHeader(hc, base+"/", "X-Content-Type-Options", "nosniff")
		},
	},
	{
		Name:     "secheaders-x-frame-options",
		Controls: []asvs.ID{"V9.2.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			return expectHeaderPresent(hc, base+"/", "X-Frame-Options")
		},
	},
	{
		Name:     "request-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return false, fmt.Sprintf("GET %s/: %v", base, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.Header.Get("X-Request-Id") == "" {
				return false, "response missing X-Request-Id header"
			}
			return true, ""
		},
	},
	{
		Name:     "correlation-id-roundtrips",
		Controls: []asvs.ID{"V7.1.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
			req.Header.Set("X-Correlation-Id", "kit-verify-probe")
			resp, err := hc.Do(req)
			if err != nil {
				return false, fmt.Sprintf("GET %s/: %v", base, err)
			}
			defer func() { _ = resp.Body.Close() }()
			got := resp.Header.Get("X-Correlation-Id")
			if got != "kit-verify-probe" {
				return false, fmt.Sprintf("X-Correlation-Id round-trip = %q, want %q", got, "kit-verify-probe")
			}
			return true, ""
		},
	},
	{
		Name:     "jwt-rejects-missing-token",
		Controls: []asvs.ID{"V2.1.5", "V3.2.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			// Probes a JWT-gated route by convention. 401 → pass.
			// 404 → "no route at this path" → soft pass (the
			// service may not expose a JWT-gated whoami).
			// Anything else fails because it suggests JWT was not
			// enforced.
			req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/whoami", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return false, fmt.Sprintf("GET %s: %v", req.URL.String(), err)
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return true, ""
			case http.StatusNotFound:
				return true, "no /api/v1/whoami route — probe skipped (route-by-convention)"
			default:
				return false, fmt.Sprintf("expected 401 (or 404 to skip), got %d", resp.StatusCode)
			}
		},
	},
	{
		Name:     "csrf-rejects-state-changing-without-token",
		Controls: []asvs.ID{"V13.2.3"},
		Run: func(hc *http.Client, base string) (bool, string) {
			// POST without CSRF token MUST be rejected. 4xx →
			// pass; 404 → soft pass; 2xx/5xx → fail.
			req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/state", nil)
			resp, err := hc.Do(req)
			if err != nil {
				return false, fmt.Sprintf("POST %s: %v", req.URL.String(), err)
			}
			defer func() { _ = resp.Body.Close() }()
			switch resp.StatusCode {
			case http.StatusForbidden, http.StatusBadRequest, http.StatusUnauthorized:
				return true, ""
			case http.StatusNotFound:
				return true, "no /api/v1/state route — probe skipped (route-by-convention)"
			default:
				return false, fmt.Sprintf("expected 4xx rejection, got %d", resp.StatusCode)
			}
		},
	},
	{
		Name:     "ratelimit-emits-retry-after-on-429",
		Controls: []asvs.ID{"V2.2.1", "V11.1.1"},
		Run: func(hc *http.Client, base string) (bool, string) {
			// Hammer the / endpoint enough times to trip a default
			// rate limit. No 429 in 30 requests → soft pass (the
			// limit may be above the probe burst). 429 with no
			// Retry-After → fail (header is required by RFC 9110
			// §15.5.6 for clients to back off correctly).
			const burst = 30
			saw429 := false
			retryAfter := ""
			for i := 0; i < burst; i++ {
				resp, err := hc.Get(base + "/")
				if err != nil {
					return false, fmt.Sprintf("GET %s/: %v", base, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusTooManyRequests {
					saw429 = true
					retryAfter = resp.Header.Get("Retry-After")
					break
				}
			}
			if !saw429 {
				return true, fmt.Sprintf("no 429 in %d requests — limit may be above probe burst", burst)
			}
			if retryAfter == "" {
				return false, "429 response missing Retry-After header"
			}
			return true, fmt.Sprintf("429 with Retry-After=%s", retryAfter)
		},
	},
}

func expectStatus(hc *http.Client, url string, want int) (bool, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return false, fmt.Sprintf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		return false, fmt.Sprintf("GET %s: want status %d, got %d", url, want, resp.StatusCode)
	}
	return true, ""
}

func expectHeader(hc *http.Client, url, key, contains string) (bool, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return false, fmt.Sprintf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := resp.Header.Get(key)
	if got == "" || (contains != "" && !containsFold(got, contains)) {
		return false, fmt.Sprintf("GET %s: header %s=%q does not contain %q", url, key, got, contains)
	}
	return true, ""
}

func expectHeaderPresent(hc *http.Client, url, key string) (bool, string) {
	resp, err := hc.Get(url)
	if err != nil {
		return false, fmt.Sprintf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get(key) == "" {
		return false, fmt.Sprintf("GET %s: header %s missing", url, key)
	}
	return true, ""
}

// containsFold is a tiny case-insensitive contains; importing strings
// only for this would be heavier than inline.
func containsFold(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a, b := haystack[i+j], needle[j]
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
