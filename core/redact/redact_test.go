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

func TestErrorChainTypes(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if got := ErrorChainTypes(nil); got != nil {
			t.Fatalf("ErrorChainTypes(nil) = %v, want nil", got)
		}
	})
	t.Run("walks unwrap chain by type", func(t *testing.T) {
		inner := fmt.Errorf("dial tenant-secret.internal: token=secret")
		wrapped := fmt.Errorf("startup: %w", inner)
		chain := ErrorChainTypes(wrapped)
		// Two-frame chain: outer wrapError + inner errorString.
		if len(chain) != 2 {
			t.Fatalf("chain depth = %d, want 2, got %v", len(chain), chain)
		}
		// Types are kit-controlled and never carry caller content;
		// confirm the inner string never appears.
		for _, entry := range chain {
			if strings.Contains(entry, "tenant-secret") || strings.Contains(entry, "token=secret") {
				t.Fatalf("ErrorChainTypes leaked payload: %v", chain)
			}
		}
	})
	t.Run("bounds runaway wrap chains", func(t *testing.T) {
		// Stack of 20 wraps; helper must cap at 16.
		err := fmt.Errorf("leaf")
		for i := 0; i < 20; i++ {
			err = fmt.Errorf("wrap %d: %w", i, err)
		}
		chain := ErrorChainTypes(err)
		if len(chain) != 16 {
			t.Fatalf("chain length = %d, want bounded to 16", len(chain))
		}
	})
}

func TestErrorChainAttr(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	err := fmt.Errorf("outer: %w", fmt.Errorf("inner tenant-secret/file"))
	logger.Error("startup", ErrorChain(err))

	out := buf.String()
	if strings.Contains(out, "tenant-secret") {
		t.Fatalf("error_chain leaked payload: %q", out)
	}
	if !strings.Contains(out, `"error_chain"`) {
		t.Fatalf("missing error_chain key: %q", out)
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
