package redact

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestStringValueRedactsPayload(t *testing.T) {
	got := StringValue("tenant-secret-route")
	if strings.Contains(got, "tenant-secret-route") {
		t.Fatalf("StringValue leaked payload: %q", got)
	}
	if !strings.Contains(got, "19 bytes") {
		t.Fatalf("StringValue should preserve length, got %q", got)
	}
	if StringValue("") != "<redacted empty>" {
		t.Fatalf("empty StringValue = %q", StringValue(""))
	}
}

func TestStringAttrRedactsPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	logger.Info("runtime", String("queue", "tenant-secret-queue"))

	out := buf.String()
	if strings.Contains(out, "tenant-secret-queue") {
		t.Fatalf("string attr leaked payload: %q", out)
	}
	if !strings.Contains(out, `"queue"`) {
		t.Fatalf("string attr missing key: %q", out)
	}
}

func TestErrorValueRedactsPayload(t *testing.T) {
	err := fmt.Errorf("outer: %w", fmt.Errorf("dial tenant-secret.internal: token=secret"))

	got := ErrorValue(err)
	for _, forbidden := range []string{"tenant-secret", "token=secret", "outer"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ErrorValue leaked %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "*errors.errorString") {
		t.Fatalf("ErrorValue should preserve root error type, got %q", got)
	}
	if ErrorValue(nil) != "<nil>" {
		t.Fatalf("nil ErrorValue = %q", ErrorValue(nil))
	}
}

func TestErrorAttrRedactsPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	logger.Error("backend", Error(fmt.Errorf("storage key tenant-secret/file.txt failed")))

	out := buf.String()
	if strings.Contains(out, "tenant-secret") {
		t.Fatalf("error attr leaked payload: %q", out)
	}
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("error attr missing key: %q", out)
	}
}

func TestPanicValueRedactsPayload(t *testing.T) {
	got := PanicValue("secret-token-123")
	if strings.Contains(got, "secret-token-123") {
		t.Fatalf("PanicValue leaked payload: %q", got)
	}
	if !strings.Contains(got, "string") {
		t.Fatalf("PanicValue should preserve panic type, got %q", got)
	}
}

func TestPanicAttrRedactsPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	logger.Error("panic", Panic("api-key-value"))

	out := buf.String()
	if strings.Contains(out, "api-key-value") {
		t.Fatalf("panic attr leaked payload: %q", out)
	}
	if !strings.Contains(out, `"panic"`) {
		t.Fatalf("panic attr missing key: %q", out)
	}
}
