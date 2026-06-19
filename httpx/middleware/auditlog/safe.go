package auditlog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	auditstore "github.com/bds421/rho-kit/observability/v2/auditlog"
)

func safePathFilter(errLogger *slog.Logger, fn func(string) bool, path string) (audit bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(errLogger, "path filter", rec)
			audit = true
		}
	}()
	return fn(path)
}

func safeStatusFilter(errLogger *slog.Logger, fn func(int) bool, status int) (audit bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(errLogger, "status filter", rec)
			audit = true
		}
	}()
	return fn(status)
}

func safeClientIP(errLogger *slog.Logger, fn func(*http.Request) string, r *http.Request) (ip string) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(errLogger, "client IP resolver", rec)
			ip = ""
		}
	}()
	return fn(r)
}

func safeActor(errLogger *slog.Logger, fn func(*http.Request) string, r *http.Request) (actor string) {
	defer func() {
		if rec := recover(); rec != nil {
			logCallbackPanic(errLogger, "actor extractor", rec)
			actor = "anonymous"
		}
	}()
	return fn(r)
}

// logCallbackPanic routes callback panics to the WithErrorLogger logger when
// configured, so path-filter/status-filter/extractor panics land in the same
// audit-health sink as emit failures rather than fragmenting across
// slog.Default(). Falls back to slog.Default() when no error logger is set.
func logCallbackPanic(errLogger *slog.Logger, callback string, rec any) {
	logger := errLogger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("auditlog middleware: callback panicked",
		"callback", callback,
		redact.Panic(rec),
		"stack", string(debug.Stack()),
	)
}

func safeAuditResource(r *http.Request) string {
	path := httpx.RequestPath(r)
	if path == "" {
		path = "/"
	}
	return safeAuditToken(path, auditstore.MaxResourceBytes, "path")
}

func safeAuditActor(actor string) string {
	if isSafeAuditToken(actor, auditstore.MaxActorBytes) {
		return actor
	}
	return "anonymous"
}

func safeAuditIPAddress(ip string) string {
	if ip == "" || len(ip) > auditstore.MaxIPAddressBytes || !utf8.ValidString(ip) {
		return ""
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	// netip.ParseAddr accepts any non-empty IPv6 zone after '%' without
	// character validation, so control characters or spaces smuggled into the
	// zone would otherwise be stored verbatim (audit-log injection). Reject any
	// zone containing control or space characters.
	for _, r := range addr.Zone() {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	return ip
}

func safeAuditToken(value string, maxBytes int, fallbackPrefix string) string {
	if isSafeAuditToken(value, maxBytes) {
		return value
	}
	if value == "" {
		return fallbackPrefix + "-empty"
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-invalid-sha256-%s", fallbackPrefix, hex.EncodeToString(sum[:])[:16])
}

func isSafeAuditToken(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
