package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

var errHealthCheckRedirectBlocked = errors.New("health: readiness redirects are disabled")

// HealthCheckOptions tunes [CheckHealth]. Zero values fall back to sensible
// defaults: localhost host, /ready path, 2-second timeout, plain HTTP.
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
	// TLS, when set, makes the probe use HTTPS with this client
	// configuration: Certificates (or GetClientCertificate) for a
	// listener that requires a client certificate, RootCAs for the
	// listener's CA and ServerName for the name its certificate carries
	// when that is not Host.
	TLS *tls.Config
}

// RunHealthCheck performs an HTTP GET against the local readiness endpoint
// and exits the process with 0 (healthy) or 1 (unhealthy). This is intended
// to be called from main() when the binary is invoked with --health,
// replacing wget-based Docker HEALTHCHECK commands.
func RunHealthCheck(port int) {
	RunHealthCheckOptions(HealthCheckOptions{Port: port})
}

// RunHealthCheckOptions is [RunHealthCheck] with options: it runs
// [CheckHealth] and exits the process with 0 or 1.
func RunHealthCheckOptions(opts HealthCheckOptions) {
	if err := CheckHealth(context.Background(), opts); err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// CheckHealth GETs the readiness endpoint once and returns nil only for
// HTTP 200. Redirects are refused.
func CheckHealth(ctx context.Context, opts HealthCheckOptions) error {
	if opts.Port <= 0 || opts.Port > 65535 {
		return fmt.Errorf("health: port %d out of range", opts.Port)
	}
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
	scheme := "http"
	if opts.TLS != nil {
		scheme = "https"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(opts.Port)) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := healthCheckHTTPClient(timeout, opts.TLS).Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func healthCheckHTTPClient(timeout time.Duration, tlsConfig *tls.Config) *http.Client {
	tr := healthCheckTransport()
	// The probe is local: a proxy from the environment must not see it.
	tr.Proxy = nil
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig.Clone()
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     tr,
		CheckRedirect: blockHealthCheckRedirect,
	}
}

func healthCheckTransport() *http.Transport {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		return tr.Clone()
	}
	return &http.Transport{
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

func blockHealthCheckRedirect(_ *http.Request, _ []*http.Request) error {
	return errHealthCheckRedirectBlocked
}
