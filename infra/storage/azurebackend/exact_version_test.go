package azurebackend

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExactVersionUsesAzureVersionIDAndPreservesCurrent(t *testing.T) {
	t.Parallel()

	client := &exactBlobClient{current: "newer-v2"}
	backend := NewWithClient(client, "container")

	current, _, err := backend.CurrentVersion(context.Background(), "objects/a")
	require.NoError(t, err)
	assert.Equal(t, "newer-v2", current.Version)

	require.NoError(t, backend.DeleteVersion(context.Background(), storage.ObjectVersion{
		Key: "objects/a", Version: "older-v1",
	}))
	assert.Equal(t, "older-v1", client.deleted)

	body, _, err := backend.GetVersion(context.Background(), current)
	require.NoError(t, err)
	require.NoError(t, body.Close())
}

func TestExactVersionFailsClosedForClientWithoutVersionAPI(t *testing.T) {
	t.Parallel()

	backend := NewWithClient(stubBlobClient{}, "container")
	_, _, err := backend.CurrentVersion(context.Background(), "objects/a")
	assert.ErrorIs(t, err, storage.ErrExactVersionUnavailable)
}

type exactBlobClient struct {
	failingBlobClient
	current string
	deleted string
}

func (client *exactBlobClient) GetPropertiesVersion(
	_ context.Context,
	_ string,
	version string,
) (blob.GetPropertiesResponse, error) {
	if version != "" && version == client.deleted {
		return blob.GetPropertiesResponse{}, &azcore.ResponseError{
			ErrorCode: "BlobNotFound", StatusCode: 404,
		}
	}
	if version == "" {
		version = client.current
	}
	return blob.GetPropertiesResponse{VersionID: &version}, nil
}

func (client *exactBlobClient) DownloadVersion(
	_ context.Context,
	_ string,
	version string,
) (blob.DownloadStreamResponse, error) {
	return blob.DownloadStreamResponse{DownloadResponse: blob.DownloadResponse{
		Body: io.NopCloser(strings.NewReader(version)), VersionID: &version,
	}}, nil
}

func (client *exactBlobClient) DeleteVersion(
	_ context.Context,
	_ string,
	version string,
) (blob.DeleteResponse, error) {
	client.deleted = version
	return blob.DeleteResponse{}, nil
}
