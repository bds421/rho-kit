package redislock

import (
	"context"
	"testing"
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

// Wave 126 removed the kit's bespoke token generator and the
// SETNX-then-GET probe; those tests went with them. The
// generate-token / orphan-window paths are now redsync's
// responsibility and exercised by the redsync upstream test suite
// plus the kit's integrationtest package against a real Redis.
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
