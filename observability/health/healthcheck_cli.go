package health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// RunHealthCheck performs an HTTP GET against the local readiness endpoint
// and exits the process with 0 (healthy) or 1 (unhealthy). This is intended
// to be called from main() when the binary is invoked with --health, replacing
// wget-based Docker HEALTHCHECK commands.
func RunHealthCheck(port int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://localhost:%d/ready", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 2 * time.Second}

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
