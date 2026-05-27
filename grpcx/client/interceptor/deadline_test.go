package interceptor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
)

func TestDeadlineUnary_InjectsWhenAbsent(t *testing.T) {
	icpt := interceptor.DeadlineUnary(100 * time.Millisecond)
	var observed time.Time
	err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			d, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("expected deadline on ctx")
			}
			observed = d
			return nil
		},
	)
	if err != nil {
		t.Fatalf("invoker: %v", err)
	}
	if observed.IsZero() {
		t.Fatalf("deadline not captured")
	}
	if time.Until(observed) <= 0 || time.Until(observed) > 200*time.Millisecond {
		t.Fatalf("deadline far from expected window: %v", time.Until(observed))
	}
}

func TestDeadlineUnary_PreservesTighterCaller(t *testing.T) {
	tight, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	icpt := interceptor.DeadlineUnary(1 * time.Hour)
	_ = icpt(tight, "/svc/Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			d, _ := ctx.Deadline()
			if time.Until(d) > 100*time.Millisecond {
				t.Fatalf("tighter caller deadline was widened: %v", time.Until(d))
			}
			return nil
		},
	)
}

func TestDeadlineUnary_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on non-positive d")
		}
	}()
	_ = interceptor.DeadlineUnary(0)
}

func TestDeadlineUnary_PropagatesInvokerError(t *testing.T) {
	want := errors.New("boom")
	icpt := interceptor.DeadlineUnary(time.Second)
	got := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(_ context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			return want
		},
	)
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}
