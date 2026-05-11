package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthCheckHTTPClient_BlocksRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ready", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := healthCheckHTTPClient(2 * time.Second).Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errHealthCheckRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want errHealthCheckRedirectBlocked", err)
	}
}

func TestHealthCheckHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = healthRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	client := healthCheckHTTPClient(2 * time.Second)
	if _, ok := client.Transport.(*http.Transport); !ok {
		t.Fatalf("transport = %T, want *http.Transport fallback", client.Transport)
	}
}

func TestResolveVersion_EnvOverride(t *testing.T) {
	t.Setenv("APP_VERSION", "2.0.0")
	if got := ResolveVersion("1.0.0"); got != "2.0.0" {
		t.Errorf("ResolveVersion = %q, want 2.0.0", got)
	}
}

func TestResolveVersion_Fallback(t *testing.T) {
	t.Setenv("APP_VERSION", "")
	if got := ResolveVersion("1.0.0"); got != "1.0.0" {
		t.Errorf("ResolveVersion = %q, want 1.0.0", got)
	}
}

func TestValidateDependencyCheck(t *testing.T) {
	valid := DependencyCheck{
		Name:  "redis-cache",
		Check: func(_ context.Context) string { return StatusHealthy },
	}
	if err := ValidateDependencyCheck(valid); err != nil {
		t.Fatalf("valid check rejected: %v", err)
	}

	cases := []DependencyCheck{
		{Name: "Bad Name", Check: valid.Check},
		{Name: "missing-check"},
		{Name: "negative-timeout", Check: valid.Check, Timeout: -time.Second},
	}
	for _, tc := range cases {
		if err := ValidateDependencyCheck(tc); err == nil {
			t.Fatalf("expected %q to be rejected", tc.Name)
		}
	}
}

func TestValidateDependencyCheck_DoesNotReflectName(t *testing.T) {
	cases := []DependencyCheck{
		{Name: "secret-token bad", Check: func(context.Context) string { return StatusHealthy }},
		{Name: "secret-token"},
		{Name: "secret-token", Check: func(context.Context) string { return StatusHealthy }, Timeout: -time.Second},
	}
	for _, tc := range cases {
		err := ValidateDependencyCheck(tc)
		if err == nil {
			t.Fatalf("expected %q to be rejected", tc.Name)
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("error reflected check name: %q", err)
		}
	}
}

func TestValidateCheckName_RejectsTooLong(t *testing.T) {
	if err := ValidateCheckName(strings.Repeat("a", MaxCheckNameLen)); err != nil {
		t.Fatalf("max length check name rejected: %v", err)
	}
	err := ValidateCheckName(strings.Repeat("a", MaxCheckNameLen+1))
	if err == nil {
		t.Fatal("expected overlong check name to be rejected")
	}
	if strings.Contains(err.Error(), "63") || strings.Contains(err.Error(), "64") {
		t.Fatalf("error leaked check name lengths: %q", err)
	}
}

func TestValidateCheckName_DoesNotReflectName(t *testing.T) {
	err := ValidateCheckName("secret-token bad")
	if err == nil {
		t.Fatal("expected invalid check name to be rejected")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error reflected check name: %q", err)
	}
}

func TestSafeCheckName(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "simple", parts: []string{"redis", "cache"}, want: "redis-cache"},
		{name: "colon and dot", parts: []string{"replica", "blue.green", "read:only"}, want: "replica-blue-green-read-only"},
		{name: "uppercase", parts: []string{"S3", "Primary"}, want: "s3-primary"},
		{name: "leading digit", parts: []string{"3rd-party"}, want: "check-3rd-party"},
		{name: "empty", parts: nil, want: "check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafeCheckName(tc.parts...)
			if got != tc.want {
				t.Fatalf("SafeCheckName() = %q, want %q", got, tc.want)
			}
			if err := ValidateCheckName(got); err != nil {
				t.Fatalf("SafeCheckName returned invalid name %q: %v", got, err)
			}
		})
	}
}

func TestSafeCheckName_TruncatesWithStableHash(t *testing.T) {
	got := SafeCheckName("replica", strings.Repeat("segment-", 80))
	if len(got) > MaxCheckNameLen {
		t.Fatalf("SafeCheckName length = %d, want <= %d", len(got), MaxCheckNameLen)
	}
	if err := ValidateCheckName(got); err != nil {
		t.Fatalf("SafeCheckName returned invalid name %q: %v", got, err)
	}
	again := SafeCheckName("replica", strings.Repeat("segment-", 80))
	if got != again {
		t.Fatalf("SafeCheckName must be stable: %q != %q", got, again)
	}
}

func TestOpaqueCheckName_HashesOpaqueParts(t *testing.T) {
	got := OpaqueCheckName("queue-depth", "email:priority.high", "tenant-secret")
	if err := ValidateCheckName(got); err != nil {
		t.Fatalf("OpaqueCheckName returned invalid name %q: %v", got, err)
	}
	if !strings.HasPrefix(got, "queue-depth-") {
		t.Fatalf("OpaqueCheckName() = %q, want queue-depth prefix", got)
	}
	if suffix := strings.TrimPrefix(got, "queue-depth-"); len(suffix) != 12 {
		t.Fatalf("OpaqueCheckName suffix length = %d, want 12", len(suffix))
	}
	for _, reflected := range []string{"email", "priority", "high", "tenant", "secret"} {
		if strings.Contains(got, reflected) {
			t.Fatalf("OpaqueCheckName reflected %q in %q", reflected, got)
		}
	}
	again := OpaqueCheckName("queue-depth", "email:priority.high", "tenant-secret")
	if got != again {
		t.Fatalf("OpaqueCheckName must be stable: %q != %q", got, again)
	}
}

func TestOpaqueCheckName_NoOpaquePartsUsesSafeName(t *testing.T) {
	got := OpaqueCheckName("S3 Primary")
	if got != "s3-primary" {
		t.Fatalf("OpaqueCheckName() = %q, want %q", got, "s3-primary")
	}
}

type healthRoundTripper func(*http.Request) (*http.Response, error)

func (f healthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestValidateChecker(t *testing.T) {
	if err := ValidateChecker(nil); err == nil {
		t.Fatal("expected nil checker to be rejected")
	}
	if err := ValidateChecker(&Checker{CacheTTL: -time.Second}); err == nil {
		t.Fatal("expected negative cache TTL to be rejected")
	}
	if err := ValidateChecker(&Checker{
		Checks: []DependencyCheck{{Name: "bad-check"}},
	}); err == nil {
		t.Fatal("expected invalid dependency check to be rejected")
	}
}

func TestChecker_AllHealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusHealthy }, Critical: true},
			{Name: "mq", Check: func(_ context.Context) string { return StatusHealthy }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusHealthy {
		t.Fatalf("status = %q, want healthy", resp.Status)
	}
	if resp.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", resp.Version)
	}
}

func TestChecker_CriticalUnhealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusUnhealthy }, Critical: true},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusUnhealthy {
		t.Fatalf("status = %q, want unhealthy", resp.Status)
	}
}

func TestChecker_NonCriticalDegrades(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusHealthy }, Critical: true},
			{Name: "mq", Check: func(_ context.Context) string { return StatusConnecting }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusDegraded {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
}

func TestChecker_InvalidStatusNormalizesToUnhealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "optional", Check: func(_ context.Context) string { return "unhelthy" }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusDegraded {
		t.Fatalf("status = %q, want degraded", resp.Status)
	}
	if got := resp.Services["optional"]; got != StatusUnhealthy {
		t.Fatalf("service status = %q, want unhealthy", got)
	}
}

func TestChecker_DuplicateInvalidStatusCannotBeMaskedByHealthy(t *testing.T) {
	hc := &Checker{
		Version: "1.0.0",
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string { return StatusHealthy }},
			{Name: "db", Check: func(_ context.Context) string { return "not-a-status" }},
		},
	}

	resp := hc.Evaluate(context.Background())
	if got := resp.Services["db"]; got != StatusUnhealthy {
		t.Fatalf("duplicate merged status = %q, want unhealthy", got)
	}
}

func TestChecker_NoChecks(t *testing.T) {
	hc := &Checker{Version: "1.0.0"}
	resp := hc.Evaluate(context.Background())
	if resp.Status != StatusHealthy {
		t.Fatalf("status = %q, want healthy", resp.Status)
	}
}

func TestChecker_CachesResults(t *testing.T) {
	callCount := 0
	hc := &Checker{
		Version:  "1.0.0",
		CacheTTL: 100 * time.Millisecond,
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string {
				callCount++
				return StatusHealthy
			}, Critical: true},
		},
	}

	hc.Evaluate(context.Background())
	hc.Evaluate(context.Background())

	if callCount != 1 {
		t.Errorf("check called %d times, want 1 (cached)", callCount)
	}
}

func TestChecker_CacheExpires(t *testing.T) {
	callCount := 0
	hc := &Checker{
		Version:  "1.0.0",
		CacheTTL: 1 * time.Millisecond,
		Checks: []DependencyCheck{
			{Name: "db", Check: func(_ context.Context) string {
				callCount++
				return StatusHealthy
			}, Critical: true},
		},
	}

	hc.Evaluate(context.Background())
	time.Sleep(5 * time.Millisecond)
	hc.Evaluate(context.Background())

	if callCount != 2 {
		t.Errorf("check called %d times, want 2 (cache expired)", callCount)
	}
}
