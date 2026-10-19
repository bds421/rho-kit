package stack

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2"
	mwrecover "github.com/bds421/rho-kit/httpx/v2/middleware/recover"
	"github.com/bds421/rho-kit/httpx/v2/middleware/secheaders"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// minimal* disables every default stage that interferes with isolating a
// single option under test (logging output, request scoping, timeout
// buffering, etc.). Stages relevant to the test under examination are left
// enabled by the caller.
func minimalOpts(extra ...Option) []Option {
	base := []Option{
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutRequestLogger(),
		WithoutTimeout(),
	}
	return append(base, extra...)
}

// TestDefault_WithCompress_CompressesEligibleResponse exercises the
// documented WithCompress wiring: an Accept-Encoding: gzip request to a
// handler emitting a JSON body above the compression threshold must come
// back gzip-encoded, and the bytes must round-trip to the original payload.
func TestDefault_WithCompress_CompressesEligibleResponse(t *testing.T) {
	body := strings.Repeat("a", 4096) // above compress.DefaultMinSize (1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	stacked := Default(handler, slog.Default(),
		minimalOpts(WithoutSecHeaders(), WithCompress())...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (WithCompress not wired?)", got)
	}

	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	defer func() { _ = gr.Close() }()
	decoded, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("reading gzip body: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(decoded), len(body))
	}
}

// TestDefault_WithoutCompress_LeavesResponseIdentity is the negative control
// for the test above: with compression off (the default), the same request
// returns identity bytes regardless of Accept-Encoding.
func TestDefault_WithoutCompress_LeavesResponseIdentity(t *testing.T) {
	body := strings.Repeat("a", 4096)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	stacked := Default(handler, slog.Default(),
		minimalOpts(WithoutSecHeaders())...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when WithCompress is not set", got)
	}
	if rec.Body.String() != body {
		t.Fatalf("body altered without WithCompress: got %d bytes, want %d", rec.Body.Len(), len(body))
	}
}

// TestDefault_WithCompress_SitsInsideRequestLogger pins the documented
// placement: compression must sit between the request logger and the
// handler, so the request logger (outside compress) sees the compressed
// wire bytes. We assert the response is compressed even with the request
// logger stage enabled.
func TestDefault_WithCompress_SitsInsideRequestLogger(t *testing.T) {
	body := strings.Repeat("b", 4096)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	stacked := Default(handler, slog.Default(),
		WithoutMetrics(),
		WithoutRequestID(),
		WithoutCorrelationID(),
		WithoutTracing(),
		WithoutLogging(),
		WithoutTimeout(),
		WithoutSecHeaders(),
		// EnableReqLogger left ON: it must wrap the compress layer.
		WithCompress(),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip with request logger enabled", got)
	}
}

// TestDefault_WithFrameOption sets a non-default X-Frame-Options via the
// typed stack field and asserts it lands on the response.
func TestDefault_WithFrameOption(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(),
		minimalOpts(WithFrameOption(secheaders.SameOrigin))...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != string(secheaders.SameOrigin) {
		t.Fatalf("X-Frame-Options = %q, want %q (WithFrameOption not wired)", got, secheaders.SameOrigin)
	}
}

// TestDefault_WithFrameOptionDefaultsToDeny is the control for the test
// above: with no WithFrameOption the stack default DENY is emitted.
func TestDefault_WithFrameOptionDefaultsToDeny(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(), minimalOpts()...)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != string(secheaders.Deny) {
		t.Fatalf("X-Frame-Options = %q, want DENY by default", got)
	}
}

// TestDefault_SecHeadersOptionsOverrideTypedFrameOption pins the FR-018
// ordering invariant documented on the stack: caller-supplied secheaders
// options are forwarded AFTER the typed FrameOption, so a caller can
// override the typed default. Here the typed field is left at the DENY
// default but a SAMEORIGIN option supplied via WithSecHeadersOptions must
// win.
func TestDefault_SecHeadersOptionsOverrideTypedFrameOption(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(),
		minimalOpts(
			// Typed default is DENY; caller option must override to SAMEORIGIN.
			WithSecHeadersOptions(secheaders.WithFrameOption(secheaders.SameOrigin)),
		)...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != string(secheaders.SameOrigin) {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN (caller secheaders option must override typed FrameOption)", got)
	}
}

// TestDefault_SecHeadersOptionsForwardArbitraryOption confirms that an
// option with no typed stack equivalent (here: a CSP override) is forwarded
// to secheaders.New verbatim.
func TestDefault_SecHeadersOptionsForwardArbitraryOption(t *testing.T) {
	const csp = "default-src 'self'"
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	stacked := Default(handler, slog.Default(),
		minimalOpts(WithSecHeadersOptions(secheaders.WithContentSecurityPolicy(csp)))...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Security-Policy"); got != csp {
		t.Fatalf("Content-Security-Policy = %q, want %q (WithSecHeadersOptions not forwarded)", got, csp)
	}
}

// TestDefault_WithRecoverMetrics verifies that a panic increments the
// http_panics_total counter registered on the supplied Metrics. The counter
// field is unexported, so we assert via the prometheus registry the Metrics
// is registered on.
func TestDefault_WithRecoverMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := mwrecover.NewMetrics(mwrecover.WithRegisterer(reg))

	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom for metrics")
	})

	stacked := Default(handler, slog.New(slog.NewTextHandler(io.Discard, nil)),
		minimalOpts(WithoutSecHeaders(), WithRecoverMetrics(metrics))...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	const expected = `
# HELP http_panics_total Number of HTTP handler panics recovered by the recover middleware.
# TYPE http_panics_total counter
http_panics_total{method="GET"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "http_panics_total"); err != nil {
		t.Fatalf("panic counter mismatch: %v", err)
	}
}

// TestDefault_WithoutRecoverMetrics is the control: with no WithRecoverMetrics
// option, no panic counter is registered, so the same panic produces zero
// http_panics_total series.
func TestDefault_WithoutRecoverMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Construct (and register) the counter, but DO NOT wire it into the stack.
	_ = mwrecover.NewMetrics(mwrecover.WithRegisterer(reg))

	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom without metrics")
	})

	stacked := Default(handler, slog.New(slog.NewTextHandler(io.Discard, nil)),
		minimalOpts(WithoutSecHeaders())...,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	stacked.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	count, err := testutil.GatherAndCount(reg, "http_panics_total")
	if err != nil {
		t.Fatalf("gathering counter: %v", err)
	}
	if count != 0 {
		t.Fatalf("http_panics_total series = %d, want 0 when metrics not wired into stack", count)
	}
}

func TestWithLogAttrs_AddsToAccessAndRequestLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Logger(r.Context(), logger).Info("in handler")
		w.WriteHeader(http.StatusNoContent)
	})
	keyID := func(r *http.Request) slog.Attr {
		if id := r.Header.Get("X-Key-ID"); id != "" {
			return slog.String("key_id", id)
		}
		return slog.Attr{}
	}

	stacked := Default(handler, logger,
		WithoutMetrics(), WithoutTracing(), WithoutSecHeaders(), WithoutTimeout(), WithLogAttrs(keyID),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Key-ID", "k1")
	stacked.ServeHTTP(httptest.NewRecorder(), req)

	if got := strings.Count(buf.String(), "key_id=k1"); got != 2 {
		t.Fatalf("key_id in %d lines, want handler and access log: %s", got, buf.String())
	}
}

func TestWithTrustedProxies_LogsTheForwardedClient(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	_, proxy, _ := net.ParseCIDR("10.0.0.0/8")
	stacked := Default(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), logger,
		WithoutMetrics(), WithoutTracing(), WithoutSecHeaders(), WithoutTimeout(), WithTrustedProxies(proxy),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set("X-Forwarded-For", "2001:db8::1")
	stacked.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(buf.String(), "2001:db8::1") {
		t.Fatalf("access log misses the forwarded client: %s", buf.String())
	}
}
