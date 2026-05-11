package logging

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/bds421/rho-kit/core/v2/redact"
)

func safeExtraAttr(logger *slog.Logger, source string, fn func(*http.Request) slog.Attr, r *http.Request) (attr slog.Attr) {
	if fn == nil {
		loggingLogger(logger).Error("logging: nil extra attribute callback skipped",
			"source", source,
		)
		return slog.Attr{}
	}
	defer func() {
		if rec := recover(); rec != nil {
			loggingLogger(logger).Error("logging: extra attribute callback panicked",
				"source", source,
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			attr = slog.Attr{}
		}
	}()
	return fn(r)
}

func safeClientIP(logger *slog.Logger, resolver func(*http.Request) string, r *http.Request) (remote string) {
	if resolver == nil {
		loggingLogger(logger).Error("logging: nil client IP resolver skipped")
		return ""
	}
	defer func() {
		if rec := recover(); rec != nil {
			loggingLogger(logger).Error("logging: client IP resolver panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			remote = ""
		}
	}()
	return resolver(r)
}

func loggingLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
