package httpx

import (
	"context"
	"testing"
)

func TestSetRequestID_RoundTrip(t *testing.T) {
	ctx := SetRequestID(context.Background(), "test-request-id")

	got := RequestID(ctx)
	if got != "test-request-id" {
		t.Errorf("RequestID() = %q, want %q", got, "test-request-id")
	}
}

func TestRequestID_EmptyContext(t *testing.T) {
	got := RequestID(context.Background())
	if got != "" {
		t.Errorf("RequestID() on empty context = %q, want empty string", got)
	}
}
