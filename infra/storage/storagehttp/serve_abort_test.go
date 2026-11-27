package storagehttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/encryption"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

// tamperedEncryptedBackend holds an encrypted object whose second segment
// was changed, so decryption fails after the first segment was served.
func tamperedEncryptedBackend(t *testing.T) storage.Storage {
	t.Helper()
	ctx := context.Background()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	raw := membackend.New()
	enc := encryption.New(raw, encryption.StaticKey(key))
	plaintext := bytes.Repeat([]byte("p"), 3*encryption.SegmentSize)
	require.NoError(t, enc.Put(ctx, "file.bin", bytes.NewReader(plaintext), storage.ObjectMeta{Size: int64(len(plaintext))}))

	rc, _, err := raw.Get(ctx, "file.bin")
	require.NoError(t, err)
	stored, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	stored[encryption.SegmentSize+encryption.SegmentSize/2] ^= 1
	require.NoError(t, raw.Put(ctx, "file.bin", bytes.NewReader(stored), storage.ObjectMeta{Size: int64(len(stored))}))
	return enc
}

func TestServeFileAbortsATamperedEncryptedObject(t *testing.T) {
	t.Parallel()
	backend := tamperedEncryptedBackend(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = ServeFile(w, r, backend, "file.bin", ServeOptions{})
	})
	for _, http2 := range []bool{false, true} {
		srv := httptest.NewUnstartedServer(handler)
		srv.EnableHTTP2 = http2
		srv.StartTLS()
		t.Cleanup(srv.Close)

		resp, err := srv.Client().Get(srv.URL)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, http2, resp.ProtoMajor == 2)
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Error(t, err, "HTTP/2 %v: a tampered object must not end like a complete one", http2)
		require.Less(t, len(got), 3*encryption.SegmentSize)
	}
}
