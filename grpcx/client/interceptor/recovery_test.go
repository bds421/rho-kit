package interceptor_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
)

// TestRecoveryUnary_ConvertsPanicToInternal verifies that a panic in the
// invoker chain (e.g. a buggy caller-supplied interceptor) is recovered and
// converted to a codes.Internal status error instead of unwinding the
// goroutine and crashing the process.
func TestRecoveryUnary_ConvertsPanicToInternal(t *testing.T) {
	icpt := interceptor.RecoveryUnary(nil)
	err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			panic("boom")
		},
	)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// TestRecoveryUnary_PassesThroughOnSuccess verifies the recovery wrapper is
// transparent when nothing panics: the invoker error (or nil) is returned
// unchanged.
func TestRecoveryUnary_PassesThroughOnSuccess(t *testing.T) {
	icpt := interceptor.RecoveryUnary(nil)

	if err := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return nil
		},
	); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	want := errors.New("boom")
	if got := icpt(context.Background(), "/svc/Method", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return want
		},
	); !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

// TestRecoveryStream_ConvertsPanicToInternal mirrors the unary case for the
// stream interceptor.
func TestRecoveryStream_ConvertsPanicToInternal(t *testing.T) {
	icpt := interceptor.RecoveryStream(nil)
	_, err := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			panic("boom")
		},
	)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}

// TestRecoveryStream_PassesThroughOnSuccess verifies the stream recovery
// wrapper returns the underlying stream + error unchanged when no panic
// occurs.
func TestRecoveryStream_PassesThroughOnSuccess(t *testing.T) {
	icpt := interceptor.RecoveryStream(nil)
	want := &fakeClientStream{ctx: context.Background()}
	got, err := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return want, nil
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != want {
		t.Fatalf("stream = %v, want %v", got, want)
	}

	wantErr := errors.New("dial failed")
	if _, gotErr := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, wantErr
		},
	); !errors.Is(gotErr, wantErr) {
		t.Fatalf("err = %v, want %v", gotErr, wantErr)
	}
}
