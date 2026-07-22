//go:build integration

package storagetest

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage/s3backend/v2"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestS3PutLargerThanMinIOStreamingChunkLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// This is the compatibility boundary that rejected the AWS SDK's optional
	// aws-chunked checksum stream with "chunk too big" for a 24 MiB lineage
	// object in a real downstream execution.
	config := startMinIO(t, "rho-kit-large-put", "minio/minio:RELEASE.2025-04-22T22-12-26Z")
	createS3Bucket(t, ctx, config)
	backend, err := s3backend.NewContext(ctx, config)
	require.NoError(t, err)

	const size = 24 << 20
	payload := bytes.Repeat([]byte("rho-kit-large-put"), size/len("rho-kit-large-put")+1)[:size]
	require.NoError(t, backend.Put(ctx, "large/object", bytes.NewReader(payload), storage.ObjectMeta{
		ContentType: "application/octet-stream", Size: size,
	}))
	reader, metadata, err := backend.Get(ctx, "large/object")
	require.NoError(t, err)
	retained, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, int64(size), metadata.Size)
	require.Equal(t, payload, retained)
}
