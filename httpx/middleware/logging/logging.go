package logging

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware"
	"github.com/bds421/rho-kit/httpx/v2/middleware/clientip"
)

// LoggerOption configures the access-log middleware.
type LoggerOption func(*loggerConfig)

type loggerConfig struct {
	clientIPResolver func(*http.Request) string
}

// WithClientIPResolver swaps in a shared client-IP resolver so the access
// log records the same IP that other middleware (rate limiter, audit log,
// authz) sees. Without this, the default resolver uses the kit's
// loopback-only trusted proxy set — which disagrees with services that
// configured WithTrustedProxies on the rate limiter.
//
// Typical wiring:
//
//	trusted, _ := clientip.ParseTrustedProxiesStrict([]string{"10.0.0.0/8"})
//	resolver := func(r *http.Request) string {
//	    return clientip.ClientIPWithTrustedProxies(r, trusted)
//	}
//	mw := logging.Logger(logger, quiet, attrs, logging.WithClientIPResolver(resolver))
//	rl := ratelimit.New(... ratelimit.WithTrustedProxies(trusted))
//
// The same resolver value SHOULD be used everywhere the request's "real"
// client IP is read, so an attacker can't cause one middleware to see a
// proxy IP and another to see the spoofed XFF.
func WithClientIPResolver(resolver func(*http.Request) string) LoggerOption {
	return func(c *loggerConfig) {
		if resolver != nil {
			c.clientIPResolver = resolver
		}
	}
}

// WithTrustedProxies is a convenience over [WithClientIPResolver] that
// installs the standard kit resolver bound to a CIDR list. Equivalent to
// passing a function that calls [clientip.ClientIPWithTrustedProxies].
func WithTrustedProxies(trusted []*net.IPNet) LoggerOption {
	trusted = cloneIPNets(trusted)
	return WithClientIPResolver(func(r *http.Request) string {
		return clientip.ClientIPWithTrustedProxies(r, trusted)
	})
}

// Logger returns middleware that logs each HTTP request with method, path,
// status, and duration.
//
// Paths in quietPaths are logged at Debug level to reduce noise from health
// checks. Trailing slashes are normalized so "/health" and "/health/" match
// the same entry. Each function in extraAttrs is called per request to add
// additional slog attributes (e.g. request ID).
//
// The "remote" attribute uses the kit's loopback-only default resolver
// unless [WithClientIPResolver] or [WithTrustedProxies] is supplied — in
// which case the access log and any other middleware sharing the resolver
// agree on what the client IP is.
func Logger(logger *slog.Logger, quietPaths []string, extraAttrs ...func(r *http.Request) slog.Attr) func(http.Handler) http.Handler {
	return LoggerWithOptions(logger, quietPaths, nil, extraAttrs...)
}

// LoggerWithOptions is the variadic-options variant of [Logger]. Kept
// distinct so the existing simple signature stays compatible with the
// hundreds of callers that don't need a resolver.
func LoggerWithOptions(logger *slog.Logger, quietPaths []string, opts []LoggerOption, extraAttrs ...func(r *http.Request) slog.Attr) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	extraAttrs = append([]func(r *http.Request) slog.Attr(nil), extraAttrs...)
	cfg := loggerConfig{clientIPResolver: clientip.ClientIP}
	for _, o := range opts {
		if o == nil {
			panic("logging: middleware option must not be nil")
		}
		o(&cfg)
	}

	quiet := make(map[string]bool, len(quietPaths))
	for _, p := range quietPaths {
		quiet[strings.TrimRight(p, "/")] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			wrapped := middleware.NewResponseRecorder(w)
			defer func() {
				recovered := recover()
				logAccessRequest(logger, cfg, quiet, wrapped, r, start, recovered != nil, extraAttrs)
				if recovered != nil {
					panic(recovered)
				}
			}()

			next.ServeHTTP(wrapped, r)
		})
	}
}

func logAccessRequest(logger *slog.Logger, cfg loggerConfig, quiet map[string]bool, wrapped *middleware.ResponseRecorder, r *http.Request, start time.Time, panicked bool, extraAttrs []func(r *http.Request) slog.Attr) {
	level := slog.LevelInfo
	path := httpx.RequestPath(r)
	if quiet[strings.TrimRight(path, "/")] {
		level = slog.LevelDebug
	}

	status := wrapped.Status()
	if panicked && !wrapped.WroteHeader() {
		status = http.StatusInternalServerError
	}

	attrs := []slog.Attr{
		slog.String("method", r.Method),
		redact.String("path", path),
		slog.Int("status", status),
		slog.Duration("duration", time.Since(start)),
		slog.String("remote", safeClientIP(logger, cfg.clientIPResolver, r)),
	}
	if panicked {
		attrs = append(attrs, slog.Bool("panicked", true))
	}
	for _, fn := range extraAttrs {
		if a := safeExtraAttr(logger, "access-log", fn, r); a.Key != "" {
			attrs = append(attrs, a)
		}
	}

	logger.LogAttrs(r.Context(), level, "request", attrs...)
}

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &net.IPNet{
			IP:   append(net.IP(nil), n.IP...),
			Mask: append(net.IPMask(nil), n.Mask...),
		})
	}
	return out
}
