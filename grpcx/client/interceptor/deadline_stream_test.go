package interceptor_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/bds421/rho-kit/grpcx/v2/client/interceptor"
)

// fakeClientStream is a minimal grpc.ClientStream whose Context()
// returns the ctx the streamer was handed, so tests can inspect the
// deadline the interceptor installed.
type fakeClientStream struct {
	grpc.ClientStream
	ctx     context.Context
	recvErr error
}

func (f *fakeClientStream) Context() context.Context { return f.ctx }

func (f *fakeClientStream) RecvMsg(any) error { return f.recvErr }

func (f *fakeClientStream) CloseSend() error { return nil }

// TestDeadlineStream_BoundsWholeStream documents the real behavior: the
// deadline the interceptor installs governs the ENTIRE stream lifetime,
// not just setup. context.WithTimeout on the streamer ctx is the ctx the
// stream runs on, so a long-lived stream is aborted with
// DeadlineExceeded once d elapses. The docstring must reflect this.
func TestDeadlineStream_BoundsWholeStream(t *testing.T) {
	const d = 50 * time.Millisecond
	icpt := interceptor.DeadlineStream(d)

	var streamerCtx context.Context
	cs, err := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			streamerCtx = ctx
			return &fakeClientStream{ctx: ctx, recvErr: io.EOF}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	dl, ok := streamerCtx.Deadline()
	if !ok {
		t.Fatalf("expected the streamer ctx to carry a deadline bounding the whole stream")
	}
	if until := time.Until(dl); until <= 0 || until > 2*d {
		t.Fatalf("deadline far from expected window: %v", until)
	}

	// The returned (wrapped) stream exposes the same bounded ctx, so the
	// deadline persists for the stream body, not only setup.
	if csDL, csOK := cs.Context().Deadline(); !csOK || !csDL.Equal(dl) {
		t.Fatalf("wrapped stream ctx deadline = (%v, %v), want %v", csDL, csOK, dl)
	}
}

// TestDeadlineStream_PreservesTighterCaller mirrors the unary behavior:
// a caller deadline tighter than now+d is preserved.
func TestDeadlineStream_PreservesTighterCaller(t *testing.T) {
	tight, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	icpt := interceptor.DeadlineStream(1 * time.Hour)

	_, err := icpt(tight, &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			dl, ok := ctx.Deadline()
			if !ok {
				t.Fatalf("expected deadline on ctx")
			}
			if time.Until(dl) > 100*time.Millisecond {
				t.Fatalf("tighter caller deadline was widened: %v", time.Until(dl))
			}
			return &fakeClientStream{ctx: ctx, recvErr: io.EOF}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
}

func TestDeadlineStream_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on non-positive d")
		}
	}()
	_ = interceptor.DeadlineStream(0)
}

// TestDeadlineStream_CancelsBoundedCtxOnTerminalRecvMsg verifies the early-
// release contract of boundedClientStream: once the wrapped stream's RecvMsg
// returns a terminal error (io.EOF on a normal close, or a real RPC error)
// the bounded context is cancelled so the deadline timer is released
// promptly rather than lingering until d elapses. The cancel must be
// idempotent across repeated RecvMsg calls after EOF.
func TestDeadlineStream_CancelsBoundedCtxOnTerminalRecvMsg(t *testing.T) {
	icpt := interceptor.DeadlineStream(time.Hour)

	cs, err := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			return &fakeClientStream{ctx: ctx, recvErr: io.EOF}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	// Before the terminal RecvMsg the bounded ctx is still live.
	select {
	case <-cs.Context().Done():
		t.Fatalf("bounded ctx cancelled before any terminal RecvMsg")
	default:
	}

	// RecvMsg returns the terminal error and must fire cancel.
	if got := cs.RecvMsg(nil); got != io.EOF {
		t.Fatalf("RecvMsg = %v, want io.EOF", got)
	}
	select {
	case <-cs.Context().Done():
		// Expected: cancel fired on terminal error.
	case <-time.After(time.Second):
		t.Fatalf("bounded ctx not cancelled after terminal RecvMsg")
	}

	// A second RecvMsg after EOF must be safe (idempotent cancel) and still
	// surface the terminal error.
	if got := cs.RecvMsg(nil); got != io.EOF {
		t.Fatalf("second RecvMsg = %v, want io.EOF", got)
	}
}

// TestDeadlineStream_DoesNotCancelOnSuccessfulRecvMsg verifies the converse:
// a successful (nil-error) RecvMsg leaves the stream live so subsequent
// messages can be received. cancel fires only on the terminal error.
func TestDeadlineStream_DoesNotCancelOnSuccessfulRecvMsg(t *testing.T) {
	icpt := interceptor.DeadlineStream(time.Hour)

	// Server-streaming: successful RecvMsg is not terminal — more messages may follow.
	cs, err := icpt(context.Background(), &grpc.StreamDesc{ServerStreams: true}, nil, "/svc/Stream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			return &fakeClientStream{ctx: ctx, recvErr: nil}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := cs.RecvMsg(nil); got != nil {
		t.Fatalf("RecvMsg = %v, want nil", got)
	}
	select {
	case <-cs.Context().Done():
		t.Fatalf("bounded ctx cancelled on a successful RecvMsg")
	default:
	}
}

func TestDeadlineStream_PropagatesStreamerError(t *testing.T) {
	want := errors.New("boom")
	icpt := interceptor.DeadlineStream(time.Second)
	_, got := icpt(context.Background(), &grpc.StreamDesc{}, nil, "/svc/Stream",
		func(_ context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, want
		},
	)
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

// TestDeadlineStream_CancelsOnClientStreamingSuccess: CloseAndRecv-style
// RPCs complete on the first successful RecvMsg; the bounded ctx must
// release immediately rather than waiting for the timeout timer.
func TestDeadlineStream_CancelsOnClientStreamingSuccess(t *testing.T) {
	icpt := interceptor.DeadlineStream(time.Hour)

	cs, err := icpt(context.Background(), &grpc.StreamDesc{ClientStreams: true}, nil, "/svc/ClientStream",
		func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
			return &fakeClientStream{ctx: ctx, recvErr: nil}, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := cs.RecvMsg(nil); got != nil {
		t.Fatalf("RecvMsg = %v, want nil", got)
	}
	select {
	case <-cs.Context().Done():
		// expected
	default:
		t.Fatalf("bounded ctx not cancelled after successful client-streaming RecvMsg")
	}
}
