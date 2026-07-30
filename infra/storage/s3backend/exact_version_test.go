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

func TestExactVersionsByPrefixPagesEveryKeyVersionAndCountsDeleteMarkers(
	t *testing.T,
) {
	t.Parallel()
	calls := 0
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			input *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			calls++
			require.Equal(t, "operations/a/", aws.ToString(input.Prefix))
			switch calls {
			case 1:
				require.Nil(t, input.KeyMarker)
				require.Nil(t, input.VersionIdMarker)
				return &s3.ListObjectVersionsOutput{
					Versions: []types.ObjectVersion{
						{Key: aws.String("operations/a/one"), VersionId: aws.String("v1")},
						{Key: aws.String("operations/a/one"), VersionId: aws.String("v2")},
					},
					DeleteMarkers: []types.DeleteMarkerEntry{
						{Key: aws.String("operations/a/deleted"), VersionId: aws.String("d1")},
					},
					IsTruncated:         aws.Bool(true),
					NextKeyMarker:       aws.String("operations/a/one"),
					NextVersionIdMarker: aws.String("v2"),
				}, nil
			case 2:
				require.Equal(t, "operations/a/one", aws.ToString(input.KeyMarker))
				require.Equal(t, "v2", aws.ToString(input.VersionIdMarker))
				return &s3.ListObjectVersionsOutput{
					Versions: []types.ObjectVersion{
						{Key: aws.String("operations/a/two"), VersionId: aws.String("v1")},
					},
				}, nil
			default:
				t.Fatalf("unexpected page %d", calls)
				return nil, nil
			}
		},
	})
	versions, err := backend.VersionsByPrefix(
		context.Background(),
		"operations/a/",
		4,
	)
	require.NoError(t, err)
	require.Equal(t, []storage.ObjectVersion{
		{Key: "operations/a/one", Version: "v1"},
		{Key: "operations/a/one", Version: "v2"},
		{Key: "operations/a/two", Version: "v1"},
	}, versions)
	require.Equal(t, 2, calls)

	calls = 0
	_, err = backend.VersionsByPrefix(
		context.Background(),
		"operations/a/",
		3,
	)
	assert.ErrorIs(t, err, storage.ErrBatchTooLarge)
}

func TestExactVersionsByPrefixRejectsUnchangedPaginationMarkers(
	t *testing.T,
) {
	t.Parallel()
	calls := 0
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			_ *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			calls++
			return &s3.ListObjectVersionsOutput{
				IsTruncated:         aws.Bool(true),
				NextKeyMarker:       aws.String("operations/a/one"),
				NextVersionIdMarker: aws.String("v1"),
			}, nil
		},
	})
	_, err := backend.VersionsByPrefix(
		context.Background(),
		"operations/a/",
		4,
	)
	assert.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
	require.Equal(t, 2, calls)
}

func TestExactVersionPagesCarrySameKeyGenerationCursorAndCountTombstones(
	t *testing.T,
) {
	t.Parallel()
	calls := 0
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			input *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			calls++
			require.Equal(t, "objects/", aws.ToString(input.Prefix))
			require.Equal(t, int32(3), aws.ToInt32(input.MaxKeys))
			switch calls {
			case 1:
				require.Nil(t, input.KeyMarker)
				require.Nil(t, input.VersionIdMarker)
				return &s3.ListObjectVersionsOutput{
					DeleteMarkers: []types.DeleteMarkerEntry{
						{Key: aws.String("objects/a"), VersionId: aws.String("deleted-v4")},
					},
					Versions: []types.ObjectVersion{
						{Key: aws.String("objects/a"), VersionId: aws.String("v3")},
						{Key: aws.String("objects/a"), VersionId: aws.String("v2")},
					},
					IsTruncated:         aws.Bool(true),
					NextKeyMarker:       aws.String("objects/a"),
					NextVersionIdMarker: aws.String("v2"),
				}, nil
			case 2:
				require.Equal(t, "objects/a", aws.ToString(input.KeyMarker))
				require.Equal(t, "v2", aws.ToString(input.VersionIdMarker))
				return &s3.ListObjectVersionsOutput{
					Versions: []types.ObjectVersion{
						{Key: aws.String("objects/a"), VersionId: aws.String("v1")},
						{Key: aws.String("objects/b"), VersionId: aws.String("v1")},
					},
				}, nil
			default:
				t.Fatalf("unexpected page %d", calls)
				return nil, nil
			}
		},
	})

	first, err := backend.VersionsPage(
		context.Background(),
		"objects/",
		storage.ExactVersionPageOptions{Limit: 3},
	)
	require.NoError(t, err)
	require.Equal(t, []storage.ObjectVersion{
		{Key: "objects/a", Version: "v3"},
		{Key: "objects/a", Version: "v2"},
	}, first.Objects)
	require.True(t, first.Truncated)
	require.NotEmpty(t, first.NextCursor)
	assert.NotContains(t, string(first.NextCursor), "objects/a")

	second, err := backend.VersionsPage(
		context.Background(),
		"objects/",
		storage.ExactVersionPageOptions{
			Limit:  3,
			Cursor: first.NextCursor,
		},
	)
	require.NoError(t, err)
	require.Equal(t, []storage.ObjectVersion{
		{Key: "objects/a", Version: "v1"},
		{Key: "objects/b", Version: "v1"},
	}, second.Objects)
	assert.False(t, second.Truncated)
	assert.Empty(t, second.NextCursor)
	require.Equal(t, 2, calls)
}

func TestExactVersionPageRejectsNonProgressingProviderCursor(t *testing.T) {
	t.Parallel()
	calls := 0
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			input *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			calls++
			nextKey := "objects/a"
			nextVersion := "v2"
			if calls == 2 {
				require.Equal(t, nextKey, aws.ToString(input.KeyMarker))
				require.Equal(t, nextVersion, aws.ToString(input.VersionIdMarker))
			}
			return &s3.ListObjectVersionsOutput{
				IsTruncated:         aws.Bool(true),
				NextKeyMarker:       aws.String(nextKey),
				NextVersionIdMarker: aws.String(nextVersion),
			}, nil
		},
	})

	first, err := backend.VersionsPage(
		context.Background(),
		"objects/",
		storage.ExactVersionPageOptions{Limit: 2},
	)
	require.NoError(t, err)
	_, err = backend.VersionsPage(
		context.Background(),
		"objects/",
		storage.ExactVersionPageOptions{
			Limit:  2,
			Cursor: first.NextCursor,
		},
	)
	require.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
	require.Equal(t, 2, calls)
}

func TestExactVersionPageCountsTombstonesAgainstPageLimit(t *testing.T) {
	t.Parallel()
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			input *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			require.Equal(t, int32(2), aws.ToInt32(input.MaxKeys))
			return &s3.ListObjectVersionsOutput{
				DeleteMarkers: []types.DeleteMarkerEntry{
					{Key: aws.String("objects/deleted"), VersionId: aws.String("d1")},
				},
				Versions: []types.ObjectVersion{
					{Key: aws.String("objects/a"), VersionId: aws.String("v1")},
					{Key: aws.String("objects/b"), VersionId: aws.String("v1")},
				},
			}, nil
		},
	})

	_, err := backend.VersionsPage(
		context.Background(),
		"objects/",
		storage.ExactVersionPageOptions{Limit: 2},
	)
	require.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
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

func TestVersionsEnumeratesEveryExactKeyGenerationAcrossPages(t *testing.T) {
	t.Parallel()

	calls := 0
	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			_ context.Context,
			input *s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			calls++
			require.Equal(t, "objects/a", aws.ToString(input.Prefix))
			if calls == 1 {
				return &s3.ListObjectVersionsOutput{
					Versions: []types.ObjectVersion{
						{Key: aws.String("objects/a"), VersionId: aws.String("v3")},
						{Key: aws.String("objects/a"), VersionId: aws.String("v2")},
					},
					IsTruncated:         aws.Bool(true),
					NextKeyMarker:       aws.String("objects/a"),
					NextVersionIdMarker: aws.String("v2"),
				}, nil
			}
			require.Equal(t, "objects/a", aws.ToString(input.KeyMarker))
			require.Equal(t, "v2", aws.ToString(input.VersionIdMarker))
			return &s3.ListObjectVersionsOutput{
				Versions: []types.ObjectVersion{
					{Key: aws.String("objects/a"), VersionId: aws.String("v1")},
					{Key: aws.String("objects/a-suffix"), VersionId: aws.String("other")},
				},
			}, nil
		},
	})

	versions, err := backend.Versions(context.Background(), "objects/a", 8)
	require.NoError(t, err)
	require.Equal(t, []storage.ObjectVersion{
		{Key: "objects/a", Version: "v3"},
		{Key: "objects/a", Version: "v2"},
		{Key: "objects/a", Version: "v1"},
	}, versions)
	require.Equal(t, 2, calls)
}

func TestVersionsRefusesTruncationAtBound(t *testing.T) {
	t.Parallel()

	backend := newTestBackend(&mockS3Client{
		versionsFn: func(
			context.Context,
			*s3.ListObjectVersionsInput,
		) (*s3.ListObjectVersionsOutput, error) {
			return &s3.ListObjectVersionsOutput{
				Versions: []types.ObjectVersion{
					{Key: aws.String("objects/a"), VersionId: aws.String("v2")},
					{Key: aws.String("objects/a"), VersionId: aws.String("v1")},
				},
			}, nil
		},
	})

	_, err := backend.Versions(context.Background(), "objects/a", 1)
	require.ErrorIs(t, err, storage.ErrBatchTooLarge)
}
