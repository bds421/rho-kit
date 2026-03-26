package httpx

import (
	"context"

	"github.com/bds421/rho-kit/core/contextutil"
)

// SetRequestID stores a request ID in the context.
//
// Deprecated: Use contextutil.SetRequestID instead.
func SetRequestID(ctx context.Context, id string) context.Context {
	return contextutil.SetRequestID(ctx, id)
}

// RequestID extracts the request ID from the context.
//
// Deprecated: Use contextutil.RequestID instead.
func RequestID(ctx context.Context) string {
	return contextutil.RequestID(ctx)
}
