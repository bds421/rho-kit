package transportdefaults

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

// DefaultMaxIdleConnsPerHost overrides the stdlib default of 2, which causes
// connection churn when a service makes many concurrent requests to a single
// downstream.
const DefaultMaxIdleConnsPerHost = 100

// MinimumTLSVersion is the kit-wide outbound TLS floor.
const MinimumTLSVersion = tls.VersionTLS12

// New clones the process default transport when it is a standard
// *http.Transport, applies kit-wide defaults, and falls back to a fresh
// stdlib-style transport when another package replaced http.DefaultTransport
// with an arbitrary RoundTripper.
func New(tlsConfig *tls.Config, idleConnTimeout time.Duration, label string) *http.Transport {
	var transport *http.Transport
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = tr.Clone()
	} else {
		transport = Fallback()
	}
	transport.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	transport.TLSClientConfig = CloneTLSConfigWithFloor(transport.TLSClientConfig, label)
	if idleConnTimeout > 0 {
		transport.IdleConnTimeout = idleConnTimeout
	}
	return transport
}

// CloneTLSConfigWithFloor returns an owned TLS config with the kit TLS floor
// enforced. Caller-set higher floors are honored.
func CloneTLSConfigWithFloor(cfg *tls.Config, _ string) *tls.Config {
	cloned, err := tlsclone.ConfigOrEmptyWithFloor(cfg, MinimumTLSVersion)
	if err != nil {
		panic("transportdefaults: TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned
}

// Fallback returns stdlib-like transport defaults without consulting
// http.DefaultTransport. It is used when the process-wide default has been
// replaced by an arbitrary RoundTripper.
func Fallback() *http.Transport {
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
