package httpx

import (
	"context"
	"log/slog"
	"testing"

	"github.com/bds421/rho-kit/observability/v2/logging"
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

func TestLogger_UsesExplicitlyStoredDefaultLogger(t *testing.T) {
	// Storing slog.Default() must still surface through Logger; presence
	// is based on FromContextOK, not identity comparison against Default().
	fallback := slog.Default().With("fallback", true)
	ctx := logging.WithContext(context.Background(), slog.Default())
	got := Logger(ctx, fallback)
	if got == fallback {
		t.Fatal("Logger should return the context-stored logger even when it is slog.Default()")
	}
}
