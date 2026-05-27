package httpx

import (
	"log/slog"
	"testing"
)

func TestLogger_NilContextUsesFallback(t *testing.T) {
	fallback := slog.Default().With("fallback", true)

	//nolint:staticcheck // Deliberately exercises the nil-safe read path.
	//lint:ignore SA1012 nil-safety contract test
	if got := Logger(nil, fallback); got != fallback {
		t.Fatal("Logger(nil, fallback) should return fallback")
	}
}

func TestLogger_NilContextWithoutFallbackUsesDefault(t *testing.T) {
	//nolint:staticcheck // Deliberately exercises the nil-safe read path.
	//lint:ignore SA1012 nil-safety contract test
	if got := Logger(nil, nil); got == nil {
		t.Fatal("Logger(nil, nil) should return a default logger")
	}
}

func TestSetLogger_NilContextUsesBackground(t *testing.T) {
	logger := slog.Default().With("stored", true)

	//nolint:staticcheck // Deliberately verifies normalization of nil context inputs.
	//lint:ignore SA1012 nil-safety contract test
	ctx := SetLogger(nil, logger)
	if ctx == nil {
		t.Fatal("SetLogger(nil, logger) returned nil context")
	}
	if got := Logger(ctx, nil); got != logger {
		t.Fatal("Logger should return logger stored from nil context")
	}
}
