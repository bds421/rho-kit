package s3backend

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExactVersionUsesS3VersionIDAndVerifiesDeletion(t *testing.T) {
	t.Parallel()

	var deleted *s3.DeleteObjectInput
	client := &mockS3Client{
		headFn: func(_ context.Context, input *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			if input.VersionId == nil {
				return &s3.HeadObjectOutput{
					VersionId: aws.String("newer-v2"), ContentLength: aws.Int64(6),
				}, nil
			}
			if aws.ToString(input.VersionId) == "older-v1" {
				return nil, &types.NoSuchKey{}
			}
			return nil, errors.New("unexpected version")
		},
		deleteFn: func(_ context.Context, input *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
			deleted = input
			return &s3.DeleteObjectOutput{}, nil
		},
		getFn: func(_ context.Context, input *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body:      io.NopCloser(strings.NewReader("newer")),
				VersionId: input.VersionId, ContentLength: aws.Int64(5),
			}, nil
		},
	}
	backend := newTestBackend(client)

	current, _, err := backend.CurrentVersion(context.Background(), "objects/a")
	require.NoError(t, err)
	assert.Equal(t, "newer-v2", current.Version)

	require.NoError(t, backend.DeleteVersion(context.Background(), storage.ObjectVersion{
		Key: "objects/a", Version: "older-v1",
	}))
	require.NotNil(t, deleted)
	assert.Equal(t, "older-v1", aws.ToString(deleted.VersionId))

	body, _, err := backend.GetVersion(context.Background(), current)
	require.NoError(t, err, "deleting the old generation must not affect the current generation")
	require.NoError(t, body.Close())
}

func TestExactVersionDeleteFailsWhenRequestedGenerationRemains(t *testing.T) {
	t.Parallel()

	client := &mockS3Client{
		headFn: func(_ context.Context, input *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{VersionId: input.VersionId}, nil
		},
	}
	backend := newTestBackend(client)
	err := backend.DeleteVersion(context.Background(), storage.ObjectVersion{
		Key: "objects/a", Version: "v1",
	})
	assert.ErrorIs(t, err, storage.ErrExactVersionStillExists)
}

func TestExactVersionRequiresS3VersionID(t *testing.T) {
	t.Parallel()

	backend := newTestBackend(&mockS3Client{
		headFn: func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{}, nil
		},
	})
	_, _, err := backend.CurrentVersion(context.Background(), "objects/a")
	assert.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
}

func TestExactVersionRequiresProviderToConfirmPinnedVersion(t *testing.T) {
	t.Parallel()

	backend := newTestBackend(&mockS3Client{
		headFn: func(context.Context, *s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{}, nil
		},
	})
	_, err := backend.StatVersion(context.Background(), storage.ObjectVersion{
		Key: "objects/a", Version: "v1",
	})
	assert.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
}
