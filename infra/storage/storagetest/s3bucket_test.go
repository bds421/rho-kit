//go:build integration

package storagetest

import (
	"bytes"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage/s3backend/v2"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestNewS3BucketIsRemovedWithItsObjects(t *testing.T) {
	endpoint := StartS3(t, "rho-kit-bucket")
	var bucket string
	t.Run("use", func(t *testing.T) {
		cfg := NewS3Bucket(t, endpoint)
		bucket = cfg.Bucket
		require.NotEqual(t, endpoint.Bucket, bucket)
		backend, err := s3backend.NewContext(t.Context(), cfg)
		require.NoError(t, err)
		require.NoError(t, backend.Put(t.Context(), "left/behind", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))
	})

	_, err := s3Client(t, endpoint).HeadBucket(t.Context(), &s3.HeadBucketInput{Bucket: &bucket})
	require.Error(t, err, "bucket %s must be gone after its test", bucket)
}
