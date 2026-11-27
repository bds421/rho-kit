package s3backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// TestPutStreamsUnseekableBodiesOverHTTP sends bodies through the real SDK
// to a plain-HTTP endpoint, where the SDK can hash only a seekable body.
func TestPutStreamsUnseekableBodiesOverHTTP(t *testing.T) {
	type request struct {
		length  int64
		hash    string
		payload []byte
	}
	var (
		mu   sync.Mutex
		last request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = request{length: r.ContentLength, hash: r.Header.Get("X-Amz-Content-Sha256"), payload: payload}
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	backend, err := NewContext(context.Background(), Config{
		Region: "us-east-1", Bucket: "bucket", Endpoint: srv.URL, ForcePathStyle: true,
		AllowInsecureEndpoint: true, AccessKeyID: "access-key", SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
	})
	require.NoError(t, err)

	payload := bytes.Repeat([]byte("p"), 100)
	cases := []struct {
		name     string
		body     io.Reader
		size     int64
		unsigned bool
	}{
		{"seekable", bytes.NewReader(payload), 100, false},
		{"unseekable with size", io.MultiReader(bytes.NewReader(payload)), 100, true},
		{"unseekable without size", io.MultiReader(bytes.NewReader(payload)), 0, false},
	}
	for _, tc := range cases {
		require.NoError(t, backend.Put(t.Context(), "key", tc.body, storage.ObjectMeta{Size: tc.size}), tc.name)
		mu.Lock()
		got := last
		mu.Unlock()
		require.EqualValues(t, len(payload), got.length, tc.name)
		require.Equal(t, payload, got.payload, tc.name)
		require.Equal(t, tc.unsigned, got.hash == "UNSIGNED-PAYLOAD", "%s: payload hash %q", tc.name, got.hash)
	}
}

func TestPutSpoolHonoursDirAndLimit(t *testing.T) {
	dir := t.TempDir()
	var (
		puts     int
		length   int64
		bodyPath string
	)
	client := &mockS3Client{putFn: func(_ context.Context, input *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
		puts++
		length = aws.ToInt64(input.ContentLength)
		if f, ok := input.Body.(*os.File); ok {
			bodyPath = f.Name()
		}
		return &s3.PutObjectOutput{}, nil
	}}
	backend := NewWithClient(client, &mockPresigner{}, "bucket", WithSpoolDir(dir), WithMaxSpoolBytes(10))

	err := backend.Put(t.Context(), "key", io.MultiReader(bytes.NewReader(make([]byte, 11))), storage.ObjectMeta{})
	require.ErrorIs(t, err, storage.ErrValidation)
	require.Zero(t, puts, "an oversized body must not be uploaded")

	require.NoError(t, backend.Put(t.Context(), "key", io.MultiReader(bytes.NewReader(make([]byte, 10))), storage.ObjectMeta{}))
	require.Equal(t, 1, puts)
	require.EqualValues(t, 10, length)
	require.True(t, strings.HasPrefix(bodyPath, dir), "spooled to %q, want under %q", bodyPath, dir)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "spool files must be removed")
}

func TestWithMaxSpoolBytesRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() { WithMaxSpoolBytes(0) })
}
