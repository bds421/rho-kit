// asvs: V8.2.2, V14.1.1
package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Health status constants.
const (
	StatusHealthy    = "healthy"
	StatusUnhealthy  = "unhealthy"
	StatusConnecting = "connecting"
	StatusDegraded   = "degraded"

	MaxCheckNameLen = 128
)

// validCheckName matches lowercase alphanumeric names with hyphens and underscores.
var validCheckName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidateCheckName returns an error if name is not a valid health check name.
// Names must start with a lowercase letter and contain only lowercase letters,
// digits, hyphens, and underscores.
func ValidateCheckName(name string) error {
	if len(name) > MaxCheckNameLen {
		return fmt.Errorf("health: invalid check name exceeds maximum length")
	}
	if !validCheckName.MatchString(name) {
		return fmt.Errorf("health: invalid check name (must match %s)", validCheckName.String())
	}
	return nil
}

// SafeCheckName builds a valid health-check name from non-sensitive name parts.
// The returned name preserves normalized input values, so do not pass tenant
// IDs, resource names, hosts, bucket names, queue names, or secrets. Use
// OpaqueCheckName when a check must remain distinct without exposing the
// underlying identifier.
func SafeCheckName(parts ...string) string {
	raw := strings.Join(parts, "\x00")
	name := normalizeCheckName(parts)
	if len(name) <= MaxCheckNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:4])
	keep := MaxCheckNameLen - len(suffix) - 1
	base := strings.TrimRight(name[:keep], "-_")
	if base == "" {
		base = "check"
	}
	return base + "-" + suffix
}

// OpaqueCheckName builds a valid health-check name from a non-sensitive prefix
// and one or more sensitive or topology-bearing identifiers. The prefix remains
// visible and the identifiers are represented by a stable hash suffix.
//
// The suffix is deterministic so duplicate checks are grouped consistently, but
// it is not a secret-bearing construction. Do not pass secrets whose equality or
// membership in a small candidate set must remain hidden.
func OpaqueCheckName(prefix string, opaqueParts ...string) string {
	name := normalizeCheckName([]string{prefix})
	if len(opaqueParts) == 0 {
		if len(name) <= MaxCheckNameLen {
			return name
		}
		return SafeCheckName(prefix)
	}

	rawParts := append([]string{prefix}, opaqueParts...)
	sum := sha256.Sum256([]byte(strings.Join(rawParts, "\x00")))
	suffix := hex.EncodeToString(sum[:6])
	keep := MaxCheckNameLen - len(suffix) - 1
	if keep < 1 {
		keep = 1
	}
	if len(name) > keep {
		name = strings.TrimRight(name[:keep], "-_")
	}
	if name == "" {
		name = "check"
	}
	return name + "-" + suffix
}

func normalizeCheckName(parts []string) string {
	var out []byte
	lastSep := true
	for _, part := range parts {
		for _, r := range part {
			c, ok := checkNameByte(r)
			if ok {
				out = append(out, c)
				lastSep = false
				continue
			}
			if !lastSep {
				out = append(out, '-')
				lastSep = true
			}
		}
		if !lastSep {
			out = append(out, '-')
			lastSep = true
		}
	}
	name := strings.Trim(string(out), "-_")
	if name == "" {
		return "check"
	}
	if name[0] < 'a' || name[0] > 'z' {
		return "check-" + name
	}
	return name
}

func checkNameByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r), true
	case r >= 'A' && r <= 'Z':
		return byte(r + ('a' - 'A')), true
	case r >= '0' && r <= '9':
		return byte(r), true
	case r == '-' || r == '_':
		return byte(r), true
	default:
		return 0, false
	}
}

// ValidateDependencyCheck returns an error when check cannot be evaluated
// safely by readiness handlers.
func ValidateDependencyCheck(check DependencyCheck) error {
	if err := ValidateCheckName(check.Name); err != nil {
		return err
	}
	if check.Check == nil {
		return fmt.Errorf("health: check requires a non-nil Check function")
	}
	if check.Timeout < 0 {
		return fmt.Errorf("health: check timeout must be >= 0")
	}
	return nil
}

// ValidateChecker returns an error when checker is nil or contains invalid
// health-check configuration.
func ValidateChecker(checker *Checker) error {
	if checker == nil {
		return fmt.Errorf("health: checker must not be nil")
	}
	if checker.CacheTTL < 0 {
		return fmt.Errorf("health: CacheTTL must be >= 0")
	}
	for i, check := range checker.Checks {
		if err := ValidateDependencyCheck(check); err != nil {
			return fmt.Errorf("health: check %d invalid: %w", i, err)
		}
	}
	return nil
}

// ResolveVersion returns the APP_VERSION environment variable if set (injected
// by the release pipeline), falling back to the compile-time version constant.
func ResolveVersion(buildVersion string) string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return buildVersion
}

// DependencyCheck describes a single health dependency.
type DependencyCheck struct {
	Name string

	// Check returns a status string: StatusHealthy, StatusUnhealthy, StatusConnecting, or StatusDegraded.
	Check func(ctx context.Context) string

	// Critical means an unhealthy result triggers HTTP 503.
	Critical bool

	// Timeout caps how long Check may run. Zero falls back to
	// [defaultCheckTimeout] (3s); negative values are invalid and are
	// rejected by readiness handler constructors. Tune lower for fast in-cluster
	// dependencies (Redis, Postgres) so kubelet probes don't queue,
	// higher for cross-region calls. The check goroutine receives a
	// cancelled context when the timeout fires.
	Timeout time.Duration
}

// Response is the standard health endpoint JSON envelope.
type Response struct {
	Status   string            `json:"status"`
	Version  string            `json:"version"`
	Services map[string]string `json:"services"`
}

// defaultCacheTTL is the duration health check results are cached before
// re-evaluating dependencies. Docker health checks run every 10s, so 5s
// ensures at most one fresh check per interval.
const defaultCacheTTL = 5 * time.Second

// Checker evaluates dependency checks and writes a JSON health response.
// Results are cached for a short TTL to prevent excessive dependency pings.
// Only one goroutine evaluates at a time; concurrent requests serve stale
// cache or wait if no cache exists yet. Waiting is context-aware — if the
// HTTP client disconnects, the waiting goroutine is released immediately.
//
// Note: this implements evaluation deduplication similar to singleflight.Group
// but with stale-cache serving for concurrent requests. A raw singleflight
// would block all waiters until the evaluation completes, which is worse
// for latency spikes. The current design returns stale results immediately
// when another goroutine is already evaluating.
//
// Checks must not be modified after the first call to Evaluate.
// The Version, CacheTTL, and Checks fields should be set before use
// and treated as immutable thereafter.
type Checker struct {
	Version  string
	CacheTTL time.Duration
	Checks   []DependencyCheck

	mu         sync.Mutex
	evaluating bool
	evalDone   chan struct{} // closed when current evaluation finishes; recreated each cycle
	cachedAt   time.Time
	cached     *cachedResult
}

type cachedResult struct {
	response Response
}

func (hc *Checker) cacheTTL() time.Duration {
	if hc.CacheTTL > 0 {
		return hc.CacheTTL
	}
	return defaultCacheTTL
}

// Evaluate runs all dependency checks and returns a [Response] with the
// aggregated health status. Results are cached for [CacheTTL] to prevent
// excessive dependency probing.
func (hc *Checker) Evaluate(ctx context.Context) Response {
	hc.mu.Lock()

	// Fast path: return cached result if still fresh.
	if hc.cached != nil && time.Since(hc.cachedAt) < hc.cacheTTL() {
		resp := hc.cached.response
		hc.mu.Unlock()
		return resp
	}

	// Another goroutine is already evaluating.
	if hc.evaluating {
		// If we have a stale cache, serve it rather than blocking.
		if hc.cached != nil {
			resp := hc.cached.response
			hc.mu.Unlock()
			return resp
		}
		// No cache yet (first request) — wait for the evaluator to finish.
		// Use a channel so we can also select on ctx.Done(), releasing the
		// goroutine immediately if the HTTP client disconnects.
		ch := hc.evalDone
		version := hc.Version // Copy under lock — hc.Version is not protected outside mu.
		hc.mu.Unlock()

		select {
		case <-ch:
			// Evaluator finished — cache is now populated.
			hc.mu.Lock()
			resp := hc.cached.response
			hc.mu.Unlock()
			return resp
		case <-ctx.Done():
			return Response{
				Status:  StatusUnhealthy,
				Version: version,
			}
		}
	}

	// We are the evaluator. Create a fresh notification channel and release the lock.
	hc.evaluating = true
	hc.evalDone = make(chan struct{})
	evalDone := hc.evalDone
	checks := make([]DependencyCheck, len(hc.Checks))
	copy(checks, hc.Checks)
	hc.mu.Unlock()

	// result is written by the check loop and consumed by the defer.
	// The defer consolidates ALL lock operations (cache update + evaluating reset)
	// into a single Lock/Unlock pair, preventing deadlock if a panic occurred
	// while the mutex was held.
	var result *cachedResult

	defer func() {
		if r := recover(); r != nil {
			slog.Error("health check panicked", redact.Panic(r))
		}
		hc.mu.Lock()
		if result != nil {
			hc.cached = result
			hc.cachedAt = time.Now()
		} else if hc.cached == nil {
			// Panic before result was set — provide a synthetic unhealthy entry
			// so waiters never nil-dereference hc.cached.
			hc.cached = &cachedResult{

				response: Response{Status: StatusUnhealthy, Version: hc.Version},
			}
			hc.cachedAt = time.Now()
		}
		// No action needed — caller gets zero-value Response on panic path.
		// The cached result is set above for future callers.
		hc.evaluating = false
		close(evalDone)
		hc.mu.Unlock()
	}()

	services := make(map[string]string, len(checks))
	overall := StatusHealthy

	// Run checks concurrently to avoid a slow dependency blocking others.
	// Each check has its own panic recovery in runCheck.
	type checkResult struct {
		name     string
		status   string
		critical bool
	}
	results := make([]checkResult, len(checks))
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for i, dc := range checks {
		go func(idx int, dc DependencyCheck) {
			defer wg.Done()
			results[idx] = checkResult{
				name:     dc.Name,
				status:   runCheck(ctx, dc),
				critical: dc.Critical,
			}
		}(i, dc)
	}
	wg.Wait()

	for _, cr := range results {
		// If duplicate check names exist, the worst status wins to prevent
		// a healthy replica from masking a critical unhealthy primary.
		if existing, ok := services[cr.name]; ok {
			if isWorseThan(cr.status, existing) {
				services[cr.name] = cr.status
			}
		} else {
			services[cr.name] = cr.status
		}
		if cr.status == StatusHealthy {
			continue
		}
		if cr.critical {
			overall = StatusUnhealthy
		} else if overall != StatusUnhealthy {
			overall = StatusDegraded
		}
	}

	resp := Response{
		Status:   overall,
		Version:  hc.Version,
		Services: services,
	}

	result = &cachedResult{response: resp}
	return resp
}

// statusSeverity maps health statuses to a numeric severity for comparison.
// Severity order: unhealthy > connecting > degraded > healthy.
var statusSeverity = map[string]int{
	StatusHealthy:    0,
	StatusDegraded:   1,
	StatusConnecting: 2,
	StatusUnhealthy:  3,
}

// isWorseThan returns true if status a is worse than status b.
func isWorseThan(a, b string) bool {
	return statusSeverity[a] > statusSeverity[b]
}

// defaultCheckTimeout is the maximum time each individual health check may run.
// Prevents a slow dependency from blocking the entire health evaluation.
// Individual checks (like HTTPCheck) may apply their own shorter timeouts.
const defaultCheckTimeout = 3 * time.Second

// runCheck executes a single DependencyCheck with panic isolation and a
// per-check timeout. If the check panics or exceeds the timeout, it returns
// StatusUnhealthy instead of blocking or crashing the evaluation.
//
// When a timeout occurs, the check goroutine may continue running until
// dc.Check returns. This is not a true leak: the channel is buffered(1) so
// the goroutine won't block on write, and checkCtx is cancelled to signal
// well-behaved checks to abort. The goroutine is cleaned up by GC once it
// returns.
func runCheck(ctx context.Context, dc DependencyCheck) string {
	timeout := dc.Timeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("health check panicked", "check", dc.Name, redact.Panic(r))
				done <- StatusUnhealthy
			}
		}()
		done <- dc.Check(checkCtx)
	}()

	select {
	case s := <-done:
		return normalizeStatus(dc.Name, s)
	case <-checkCtx.Done():
		slog.Warn("health check timed out", "check", dc.Name, "timeout", timeout)
		return StatusUnhealthy
	}
}

func normalizeStatus(checkName, status string) string {
	switch status {
	case StatusHealthy, StatusUnhealthy, StatusConnecting, StatusDegraded:
		return status
	default:
		slog.Warn("health check returned invalid status",
			"check", checkName,
			"status", status,
		)
		return StatusUnhealthy
	}
}
