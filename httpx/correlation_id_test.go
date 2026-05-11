package httpx

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

func TestPropagateHTTP(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "propagated-id")
	req := httptest.NewRequest("GET", "/", nil)

	PropagateHTTP(ctx, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "propagated-id" {
		t.Errorf("header = %q, want %q", got, "propagated-id")
	}
}

func TestPropagateHTTP_OverwritesExistingHeader(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "from-context")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Correlation-Id", "pre-existing-value")

	PropagateHTTP(ctx, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "from-context" {
		t.Errorf("header = %q, want %q; PropagateHTTP should overwrite pre-existing header", got, "from-context")
	}
}

func TestPropagateHTTP_NoCorrelationID(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/", nil)

	PropagateHTTP(ctx, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "" {
		t.Errorf("header should be empty when no correlation ID in context, got %q", got)
	}
}

func TestPropagateHTTP_NilRequestNoops(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "propagated-id")

	PropagateHTTP(ctx, nil)
}

func TestPropagateHTTP_NilContextNoops(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)

	//nolint:staticcheck // Deliberately exercises the nil-safe propagation path.
	PropagateHTTP(nil, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "" {
		t.Errorf("header should be empty for nil context, got %q", got)
	}
}

func TestPropagateHTTP_PreservesExistingHeaderWhenNoContext(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Correlation-Id", "existing-id")

	PropagateHTTP(ctx, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "existing-id" {
		t.Errorf("header = %q, want %q; pre-existing header should be preserved when context has no ID", got, "existing-id")
	}
}

func TestPropagateHTTP_SkipsInvalidContextValue(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "alice@example.com/reset/token=secret")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Correlation-Id", "existing-id")

	PropagateHTTP(ctx, req)

	if got := req.Header.Get("X-Correlation-Id"); got != "existing-id" {
		t.Errorf("header = %q, want existing-id", got)
	}
}

func TestPropagateMessageHeader(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "msg-correlation-id")

	key, value := PropagateMessageHeader(ctx)

	if key != "X-Correlation-Id" {
		t.Errorf("key = %q, want %q", key, "X-Correlation-Id")
	}
	if value != "msg-correlation-id" {
		t.Errorf("value = %q, want %q", value, "msg-correlation-id")
	}
}

func TestPropagateMessageHeader_SkipsInvalidContextValue(t *testing.T) {
	ctx := contextutil.SetCorrelationID(context.Background(), "alice@example.com/reset/token=secret")

	key, value := PropagateMessageHeader(ctx)

	if key != "" || value != "" {
		t.Errorf("expected empty key/value for invalid context ID, got (%q, %q)", key, value)
	}
}

func TestPropagateMessageHeader_NoCorrelationID(t *testing.T) {
	ctx := context.Background()

	key, value := PropagateMessageHeader(ctx)

	if key != "" || value != "" {
		t.Errorf("expected empty key/value, got (%q, %q)", key, value)
	}
}
