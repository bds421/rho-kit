package redislock

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type releaseContextKey struct{}

type recordingLock struct {
	value  any
	ctxErr error
}

func (l *recordingLock) Release(ctx context.Context) error {
	l.value = ctx.Value(releaseContextKey{})
	l.ctxErr = ctx.Err()
	return nil
}

func (*recordingLock) Extend(context.Context) (bool, error) {
	return true, nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestGenerateTokenReturnsErrorOnRandomFailure(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = failingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	token, err := generateToken()
	if err == nil {
		t.Fatal("expected random failure")
	}
	if token != "" {
		t.Fatalf("token = %q, want empty on error", token)
	}
	if !strings.Contains(err.Error(), "generate lock token") {
		t.Fatalf("error = %v, want generate lock token context", err)
	}
}

func TestLockerAcquireReturnsRandomFailure(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = failingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	lc := NewLocker(client, WithTTL(time.Minute))
	lock, ok, err := lc.Acquire(context.Background(), "k")
	if err == nil {
		t.Fatal("expected random failure")
	}
	if lock != nil || ok {
		t.Fatalf("Acquire = lock=%v ok=%v, want nil false", lock, ok)
	}
	if !strings.Contains(err.Error(), "generate lock token") {
		t.Fatalf("error = %v, want generate lock token context", err)
	}
}

func TestReleaseAndJoinPreservesValuesAfterCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), releaseContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	l := &recordingLock{}
	var retErr error
	releaseAndJoin(ctx, l, &retErr)

	if retErr != nil {
		t.Fatalf("retErr = %v, want nil", retErr)
	}
	if l.value != "trace-123" {
		t.Fatalf("release context value = %v, want trace-123", l.value)
	}
	if l.ctxErr != nil {
		t.Fatalf("release context inherited cancellation: %v", l.ctxErr)
	}
}
