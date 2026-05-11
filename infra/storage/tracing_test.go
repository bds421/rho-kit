package storage

import (
	"fmt"
	"strings"
	"testing"
)

func TestSpanErrorDescriptionRedactsRuntimeError(t *testing.T) {
	err := fmt.Errorf("upload failed for tenant-secret/object.txt: %w", errSecretBackend("token=secret"))

	got := SpanErrorDescription(err)
	for _, forbidden := range []string{"tenant-secret", "object.txt", "token=secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("SpanErrorDescription leaked %q in %q", forbidden, got)
		}
	}
	if got == "" || !strings.Contains(got, "errSecretBackend") {
		t.Fatalf("SpanErrorDescription should preserve leaf error type, got %q", got)
	}
}

type errSecretBackend string

func (e errSecretBackend) Error() string { return string(e) }
