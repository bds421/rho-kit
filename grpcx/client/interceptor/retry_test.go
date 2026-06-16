package interceptor_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// fastPolicy retries up to n times with negligible (but valid) backoff
// so retry tests run quickly.
func fastPolicy(n int) retry.Policy {
	return retry.Policy{
		MaxRetries: n,
		BaseDelay:  time.Microsecond,
		MaxDelay:   time.Millisecond,
		Factor:     1,
	}
}

func TestRetryUnary_RetriesRetryableCode(t *testing.T) {
	icpt := interceptor.RetryUnary(interceptor.WithRetryPolicy(fastPolicy(3)))
	var attempts int
	err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			attempts++
			return status.Error(codes.Unavailable, "try again")
		},
	)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("final code = %v, want Unavailable", status.Code(err))
	}
	if attempts != 4 { // 1 initial + 3 retries
		t.Fatalf("attempts = %d, want 4", attempts)
	}
}

// TestRetryUnary_DoesNotRetryOversizedMessage proves that a permanent
// ResourceExhausted caused by gRPC's client-side message-size limit is
// NOT retried, even though ResourceExhausted is in DefaultRetryableCodes.
// Retrying is guaranteed to fail (same client, same limits) and only
// burns the retry budget.
func TestRetryUnary_DoesNotRetryOversizedMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "received larger than max",
			err:  status.Error(codes.ResourceExhausted, "grpc: received message larger than max (5000000 vs. 4194304)"),
		},
		{
			name: "message too large",
			err:  status.Error(codes.ResourceExhausted, "grpc: message too large (5000000 bytes)"),
		},
		{
			name: "decompression larger than max",
			err:  status.Error(codes.ResourceExhausted, "grpc: received message after decompression larger than max (5000000 vs. 4194304)"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			icpt := interceptor.RetryUnary(interceptor.WithRetryPolicy(fastPolicy(3)))
			var attempts int
			err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
				func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
					attempts++
					return tc.err
				},
			)
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("final code = %v, want ResourceExhausted", status.Code(err))
			}
			if attempts != 1 {
				t.Fatalf("oversized-message error was retried: attempts = %d, want 1", attempts)
			}
		})
	}
}

// TestRetryUnary_RetriesRateLimitResourceExhausted confirms the
// permanence skip is narrow: a genuine rate-limit ResourceExhausted
// (no message-size signal) is still retried.
func TestRetryUnary_RetriesRateLimitResourceExhausted(t *testing.T) {
	icpt := interceptor.RetryUnary(interceptor.WithRetryPolicy(fastPolicy(2)))
	var attempts int
	err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			attempts++
			return status.Error(codes.ResourceExhausted, "rate limit exceeded")
		},
	)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("final code = %v, want ResourceExhausted", status.Code(err))
	}
	if attempts != 3 { // 1 initial + 2 retries
		t.Fatalf("rate-limit error not retried: attempts = %d, want 3", attempts)
	}
}
