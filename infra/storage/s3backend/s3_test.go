package s3backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
)

// mockS3Client implements S3Client for unit tests.
type mockS3Client struct {
	putFn    func(ctx context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error)
	getFn    func(ctx context.Context, input *s3.GetObjectInput) (*s3.GetObjectOutput, error)
	deleteFn func(ctx context.Context, input *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
	headFn   func(ctx context.Context, input *s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
	headBFn  func(ctx context.Context, input *s3.HeadBucketInput) (*s3.HeadBucketOutput, error)
	listFn   func(ctx context.Context, input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
	copyFn   func(ctx context.Context, input *s3.CopyObjectInput) (*s3.CopyObjectOutput, error)
}

func (m *mockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if m.putFn != nil {
		return m.putFn(ctx, params)
	}
	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getFn != nil {
		return m.getFn(ctx, params)
	}
	return nil, &types.NoSuchKey{}
}

func (m *mockS3Client) DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, params)
	}
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockS3Client) HeadObject(ctx context.Context, params *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if m.headFn != nil {
		return m.headFn(ctx, params)
	}
	return nil, &types.NotFound{}
}

func (m *mockS3Client) HeadBucket(ctx context.Context, params *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if m.headBFn != nil {
		return m.headBFn(ctx, params)
	}
	return &s3.HeadBucketOutput{}, nil
}

func (m *mockS3Client) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if m.listFn != nil {
		return m.listFn(ctx, params)
	}
	return &s3.ListObjectsV2Output{}, nil
}

func (m *mockS3Client) CopyObject(ctx context.Context, params *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	if m.copyFn != nil {
		return m.copyFn(ctx, params)
	}
	return &s3.CopyObjectOutput{}, nil
}

// mockPresigner implements S3Presigner for unit tests.
type mockPresigner struct {
	getURL string
	putURL string
	err    error
}

func (m *mockPresigner) PresignGetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &v4.PresignedHTTPRequest{URL: m.getURL}, nil
}

func (m *mockPresigner) PresignPutObject(_ context.Context, _ *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &v4.PresignedHTTPRequest{URL: m.putURL}, nil
}

func newTestBackend(client *mockS3Client, opts ...Option) *S3Backend {
	return NewWithClient(client, &mockPresigner{getURL: "https://presigned-get", putURL: "https://presigned-put"}, "test-bucket", opts...)
}

func TestS3Backend_Put(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("uploads content successfully", func(t *testing.T) {
		t.Parallel()
		var capturedInput *s3.PutObjectInput
		client := &mockS3Client{
			putFn: func(_ context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
				capturedInput = input
				return &s3.PutObjectOutput{}, nil
			},
		}
		b := newTestBackend(client)

		err := b.Put(ctx, "test/file.txt", bytes.NewReader([]byte("hello")), storage.ObjectMeta{
			ContentType: "text/plain",
			Size:        5,
		})
		require.NoError(t, err)

		assert.Equal(t, "test-bucket", aws.ToString(capturedInput.Bucket))
		assert.Equal(t, "test/file.txt", aws.ToString(capturedInput.Key))
		assert.Equal(t, "text/plain", aws.ToString(capturedInput.ContentType))
		assert.Equal(t, int64(5), aws.ToInt64(capturedInput.ContentLength))
	})

	t.Run("defaults content type to application/octet-stream", func(t *testing.T) {
		t.Parallel()
		var capturedInput *s3.PutObjectInput
		client := &mockS3Client{
			putFn: func(_ context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
				capturedInput = input
				return &s3.PutObjectOutput{}, nil
			},
		}
		b := newTestBackend(client)

		err := b.Put(ctx, "file.bin", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
		require.NoError(t, err)
		assert.Equal(t, "application/octet-stream", aws.ToString(capturedInput.ContentType))
	})

	t.Run("returns error on S3 failure", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			putFn: func(_ context.Context, _ *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
				return nil, errors.New("network error")
			},
		}
		b := newTestBackend(client)

		err := b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "s3backend: put")
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{})

		err := b.Put(ctx, "", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.Error(t, err)
	})

	t.Run("applies validators with known size", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{}, WithValidators(storage.MaxFileSize(3)))

		// When Size is declared and exceeds max, the validator rejects immediately.
		err := b.Put(ctx, "big.txt", bytes.NewReader([]byte("toolong")), storage.ObjectMeta{Size: 7})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
	})
}

func TestS3Backend_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns content and metadata", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			getFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
				return &s3.GetObjectOutput{
					Body:          io.NopCloser(bytes.NewReader([]byte("hello"))),
					ContentType:   aws.String("text/plain"),
					ContentLength: aws.Int64(5),
					Metadata:      map[string]string{"author": "test"},
				}, nil
			},
		}
		b := newTestBackend(client)

		rc, meta, err := b.Get(ctx, "file.txt")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()

		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, []byte("hello"), got)
		assert.Equal(t, "text/plain", meta.ContentType)
		assert.Equal(t, int64(5), meta.Size)
		assert.Equal(t, "test", meta.Custom["author"])
	})

	t.Run("returns ErrObjectNotFound on NoSuchKey", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			getFn: func(_ context.Context, _ *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
				return nil, &types.NoSuchKey{}
			},
		}
		b := newTestBackend(client)

		_, _, err := b.Get(ctx, "missing")
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrObjectNotFound))
	})
}

func TestS3Backend_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("deletes successfully", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{})

		err := b.Delete(ctx, "file.txt")
		assert.NoError(t, err)
	})

	t.Run("returns error on S3 failure", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			deleteFn: func(_ context.Context, _ *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
				return nil, errors.New("access denied")
			},
		}
		b := newTestBackend(client)

		err := b.Delete(ctx, "file.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "s3backend: delete")
	})
}

func TestS3Backend_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns true when object exists", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			headFn: func(_ context.Context, _ *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
				return &s3.HeadObjectOutput{}, nil
			},
		}
		b := newTestBackend(client)

		ok, err := b.Exists(ctx, "file.txt")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("returns false when object not found", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{}) // default headFn returns NotFound

		ok, err := b.Exists(ctx, "missing")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestS3Backend_PresignGetURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns presigned URL", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{getURL: "https://example.com/presigned"}, "bucket")

		url, err := b.PresignGetURL(ctx, "file.txt", 15*time.Minute)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/presigned", url)
	})

	t.Run("rejects non-positive TTL", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{}, "bucket")

		_, err := b.PresignGetURL(ctx, "file.txt", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TTL must be positive")
	})

	t.Run("rejects TTL exceeding maximum", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{}, "bucket")

		_, err := b.PresignGetURL(ctx, "file.txt", 2*time.Hour)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum")
	})
}

func TestS3Backend_PresignPutURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("returns presigned URL", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{putURL: "https://example.com/upload"}, "bucket")

		url, err := b.PresignPutURL(ctx, "file.txt", 15*time.Minute, storage.ObjectMeta{ContentType: "image/png"})
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/upload", url)
	})

	t.Run("rejects TTL exceeding maximum", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{}, "bucket")

		_, err := b.PresignPutURL(ctx, "file.txt", 24*time.Hour, storage.ObjectMeta{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum")
	})
}

func TestS3Backend_HealthCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("healthy when HeadBucket succeeds", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{})

		check := HealthCheck(b)
		assert.Equal(t, "healthy", check.Check(ctx))
		assert.False(t, check.Critical)
	})

	t.Run("unhealthy when HeadBucket fails", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			headBFn: func(_ context.Context, _ *s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
				return nil, errors.New("connection refused")
			},
		}
		b := newTestBackend(client)

		check := CriticalHealthCheck(b)
		result := check.Check(ctx)
		assert.Equal(t, "unhealthy", result)
		assert.True(t, check.Critical)
	})
}

func TestS3Backend_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("lists objects with prefix", func(t *testing.T) {
		t.Parallel()
		now := time.Now().Truncate(time.Second)
		client := &mockS3Client{
			listFn: func(_ context.Context, input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
				return &s3.ListObjectsV2Output{
					Contents: []types.Object{
						{Key: aws.String("uploads/a.txt"), Size: aws.Int64(10), LastModified: &now},
						{Key: aws.String("uploads/b.txt"), Size: aws.Int64(20), LastModified: &now},
					},
					IsTruncated: aws.Bool(false),
				}, nil
			},
		}
		b := newTestBackend(client)

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "uploads/", storage.ListOptions{}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.Len(t, results, 2)
		assert.Equal(t, "uploads/a.txt", results[0].Key)
		assert.Equal(t, int64(10), results[0].Size)
		assert.Equal(t, now, results[0].ModTime)
	})

	t.Run("respects MaxKeys", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			listFn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
				return &s3.ListObjectsV2Output{
					Contents: []types.Object{
						{Key: aws.String("a.txt"), Size: aws.Int64(1)},
						{Key: aws.String("b.txt"), Size: aws.Int64(2)},
						{Key: aws.String("c.txt"), Size: aws.Int64(3)},
					},
					IsTruncated: aws.Bool(false),
				}, nil
			},
		}
		b := newTestBackend(client)

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "", storage.ListOptions{MaxKeys: 2}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.Len(t, results, 2)
	})

	t.Run("handles pagination", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		client := &mockS3Client{
			listFn: func(_ context.Context, input *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
				callCount++
				if callCount == 1 {
					return &s3.ListObjectsV2Output{
						Contents:              []types.Object{{Key: aws.String("a.txt"), Size: aws.Int64(1)}},
						IsTruncated:           aws.Bool(true),
						NextContinuationToken: aws.String("token1"),
					}, nil
				}
				return &s3.ListObjectsV2Output{
					Contents:    []types.Object{{Key: aws.String("b.txt"), Size: aws.Int64(2)}},
					IsTruncated: aws.Bool(false),
				}, nil
			},
		}
		b := newTestBackend(client)

		var results []storage.ObjectInfo
		for info, err := range b.List(ctx, "", storage.ListOptions{}) {
			require.NoError(t, err)
			results = append(results, info)
		}

		assert.Len(t, results, 2)
		assert.Equal(t, 2, callCount)
	})

	t.Run("yields error on S3 failure", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			listFn: func(_ context.Context, _ *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
				return nil, errors.New("access denied")
			},
		}
		b := newTestBackend(client)

		for _, err := range b.List(ctx, "", storage.ListOptions{}) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "s3backend: list")
			break
		}
	})
}

func TestS3Backend_Copy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("copies object with correct CopySource", func(t *testing.T) {
		t.Parallel()
		var capturedInput *s3.CopyObjectInput
		client := &mockS3Client{
			copyFn: func(_ context.Context, input *s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
				capturedInput = input
				return &s3.CopyObjectOutput{}, nil
			},
		}
		b := newTestBackend(client)

		err := b.Copy(ctx, "src/file.txt", "dst/file.txt")
		require.NoError(t, err)

		assert.Equal(t, "test-bucket", aws.ToString(capturedInput.Bucket))
		assert.Equal(t, "test-bucket/src%2Ffile.txt", aws.ToString(capturedInput.CopySource))
		assert.Equal(t, "dst/file.txt", aws.ToString(capturedInput.Key))
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{})

		err := b.Copy(ctx, "", "dst.txt")
		assert.Error(t, err)
	})

	t.Run("returns error on S3 failure", func(t *testing.T) {
		t.Parallel()
		client := &mockS3Client{
			copyFn: func(_ context.Context, _ *s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
				return nil, errors.New("not found")
			},
		}
		b := newTestBackend(client)

		err := b.Copy(ctx, "src.txt", "dst.txt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "s3backend: copy")
	})
}

func TestS3Backend_URL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("custom endpoint uses path-style", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{}, "my-bucket", WithConfig(S3Config{
			Endpoint: "https://cdn.example.com",
			Bucket:   "my-bucket",
		}))

		u, err := b.URL(ctx, "photos/cat.jpg")
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/my-bucket/photos/cat.jpg", u)
	})

	t.Run("AWS uses virtual-hosted style", func(t *testing.T) {
		t.Parallel()
		b := NewWithClient(&mockS3Client{}, &mockPresigner{}, "my-bucket", WithConfig(S3Config{
			Region: "eu-central-1",
			Bucket: "my-bucket",
		}))

		u, err := b.URL(ctx, "photos/cat.jpg")
		require.NoError(t, err)
		assert.Equal(t, "https://my-bucket.s3.eu-central-1.amazonaws.com/photos/cat.jpg", u)
	})

	t.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		b := newTestBackend(&mockS3Client{})

		_, err := b.URL(ctx, "")
		assert.Error(t, err)
	})
}

func TestNewPanicsOnEmptyBucket(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		NewWithClient(&mockS3Client{}, &mockPresigner{}, "")
	})
}
