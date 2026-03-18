package health

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"
)

// Health status constants.
const (
	StatusHealthy    = "healthy"
	StatusUnhealthy  = "unhealthy"
	StatusConnecting = "connecting"
	StatusDegraded   = "degraded"
)

// validCheckName matches lowercase alphanumeric names with hyphens and underscores.
var validCheckName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidateCheckName returns an error if name is not a valid health check name.
// Names must start with a lowercase letter and contain only lowercase letters,
// digits, hyphens, and underscores.
func ValidateCheckName(name string) error {
	if !validCheckName.MatchString(name) {
		return fmt.Errorf("health: invalid check name %q (must match %s)", name, validCheckName.String())
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
			slog.Error("health check panicked", "panic", r)
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
	checkCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("health check panicked", "check", dc.Name, "panic", r)
				done <- StatusUnhealthy
			}
		}()
		done <- dc.Check(checkCtx)
	}()

	select {
	case s := <-done:
		return s
	case <-checkCtx.Done():
		slog.Warn("health check timed out", "check", dc.Name, "timeout", defaultCheckTimeout)
		return StatusUnhealthy
	}
}
