package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// HealthCheckOptions tunes [RunHealthCheckOptions]. Zero values fall back
// to sensible defaults: localhost host, /ready path, 2-second timeout.
type HealthCheckOptions struct {
	// Host overrides the bind host. Defaults to "localhost". Use the
	// container's hostname if the readiness endpoint is bound on a
	// non-loopback address.
	Host string
	// Port is the readiness endpoint port. Required.
	Port int
	// Path is the readiness path. Defaults to "/ready". Override for
	// services that mount readiness on a non-standard path.
	Path string
	// Timeout caps the entire probe. Defaults to 2 seconds.
	Timeout time.Duration
}

// RunHealthCheck performs an HTTP GET against the local readiness endpoint
// and exits the process with 0 (healthy) or 1 (unhealthy). This is intended
// to be called from main() when the binary is invoked with --health,
// replacing wget-based Docker HEALTHCHECK commands.
func RunHealthCheck(port int) {
	RunHealthCheckOptions(HealthCheckOptions{Port: port})
}

// RunHealthCheckOptions is the variant that accepts an options struct.
// Use this when the readiness endpoint is on a non-default path or host.
func RunHealthCheckOptions(opts HealthCheckOptions) {
	host := opts.Host
	if host == "" {
		host = "localhost"
	}
	path := opts.Path
	if path == "" {
		path = "/ready"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s:%d%s", host, opts.Port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: timeout}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
