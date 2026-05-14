package s3backend

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

type smithyErr struct {
	code    string
	message string
}

func (e *smithyErr) Error() string                 { return e.code + ": " + e.message }
func (e *smithyErr) ErrorCode() string             { return e.code }
func (e *smithyErr) ErrorMessage() string          { return e.message }
func (e *smithyErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

func TestTranslateCapacity(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		size     int64
		wantCap  bool
		wantWrap bool
	}{
		{"nil", nil, 0, false, false},
		{"non-smithy", errors.New("plain"), 0, false, false},
		{"EntityTooLarge", &smithyErr{code: "EntityTooLarge"}, 100, true, true},
		{"InvalidRequest with size", &smithyErr{code: "InvalidRequest"}, 100, true, true},
		{"InvalidRequest no size", &smithyErr{code: "InvalidRequest"}, 0, false, false},
		// Generic 503 (ServiceUnavailable) is no longer mapped to
		// ErrInsufficientCapacity: AWS uses it for regional outage,
		// throttling, partial maintenance, etc., and the previous
		// mapping would steer operators to a "bucket full" runbook for
		// transient failures.
		{"ServiceUnavailable with size", &smithyErr{code: "ServiceUnavailable"}, 1024, false, false},
		{"ServiceUnavailable no size", &smithyErr{code: "ServiceUnavailable"}, 0, false, false},
		{"OtherCode", &smithyErr{code: "Throttling"}, 100, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateS3Capacity(tc.err, tc.size)
			if !tc.wantCap {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.True(t, errors.Is(got, storage.ErrInsufficientCapacity), "must chain to ErrInsufficientCapacity: %v", got)
			assert.True(t, apperror.IsStorageFull(got))
		})
	}
}

func TestPut_EntityTooLargeReturnsInsufficientCapacity(t *testing.T) {
	client := &mockS3Client{
		putFn: func(_ context.Context, _ *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			return nil, &smithyErr{code: "EntityTooLarge", message: "Your proposed upload exceeds the maximum allowed size"}
		},
	}
	b := NewWithClient(client, &mockPresigner{}, "bucket")

	err := b.Put(context.Background(), "k.bin", bytes.NewReader([]byte("payload")), storage.ObjectMeta{Size: 7})
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrInsufficientCapacity), "got: %v", err)
}

// TestPut_GenericServiceUnavailableIsNotStorageFull asserts that S3's
// generic 503 (regional outage / throttling / maintenance) flows through
// as a transient backend error rather than being misclassified as
// STORAGE_FULL — which would route operators to a capacity runbook for
// what is actually a transient dependency failure.
func TestPut_GenericServiceUnavailableIsNotStorageFull(t *testing.T) {
	client := &mockS3Client{
		putFn: func(_ context.Context, _ *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			return nil, &smithyErr{code: "ServiceUnavailable", message: "we are unable to process your request"}
		},
	}
	b := NewWithClient(client, &mockPresigner{}, "bucket")

	err := b.Put(context.Background(), "k.bin", bytes.NewReader([]byte("payload")), storage.ObjectMeta{Size: 7})
	require.Error(t, err)
	assert.False(t, errors.Is(err, storage.ErrInsufficientCapacity),
		"generic 503 must not be reported as ErrInsufficientCapacity: %v", err)
	assert.False(t, apperror.IsStorageFull(err),
		"generic 503 must not advertise CodeStorageFull: %v", err)
}

// TestPut_EntityTooLargeErrIsStorageFull verifies the classified backend
// error advertises CodeStorageFull so HTTP/gRPC transport adapters can
// emit 507 / RESOURCE_EXHAUSTED. The httpx integration test that asserts
// the 507 status lives in httpx/apperror_status_test.go to keep this
// module's dep graph lean.
func TestPut_EntityTooLargeErrIsStorageFull(t *testing.T) {
	client := &mockS3Client{
		putFn: func(_ context.Context, _ *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
			return nil, &smithyErr{code: "EntityTooLarge", message: "too big"}
		},
	}
	b := NewWithClient(client, &mockPresigner{}, "bucket")

	err := b.Put(context.Background(), "k.bin", bytes.NewReader([]byte("payload")), storage.ObjectMeta{Size: 7})
	require.Error(t, err)
	require.True(t, apperror.IsStorageFull(err), "Put err = %v", err)
}
