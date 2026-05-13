package azurebackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

type fakeTokenCredential struct{}

func (fakeTokenCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestNewWithClient_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(nil, "container")
	})
}

func TestNewWithClient_PanicsOnEmptyContainer(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(stubBlobClient{}, "")
	})
}

func TestNewWithClient_PanicsOnNilOption(t *testing.T) {
	t.Parallel()
	assertPanics(t, func() {
		NewWithClient(stubBlobClient{}, "container", nil)
	})
}

func TestNewWithTokenCredential_DoesNotRequireAccountKey(t *testing.T) {
	t.Parallel()

	b, err := NewWithTokenCredential(AzureConfig{
		AccountName:   "account",
		ContainerName: "container",
	}, fakeTokenCredential{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("expected backend")
	}
}

func TestNewWithTokenCredential_RejectsNilCredential(t *testing.T) {
	t.Parallel()

	_, err := NewWithTokenCredential(AzureConfig{
		AccountName:   "account",
		ContainerName: "container",
	}, nil)

	requireError(t, err)
}

func TestAzureBackend_InvalidReceiverSafety(t *testing.T) {
	t.Parallel()

	var nilBackend *AzureBackend
	if nilBackend.Healthy() {
		t.Fatal("nil backend must not report healthy")
	}
	if (&AzureBackend{}).Healthy() {
		t.Fatal("zero backend must not report healthy")
	}
}

func TestAzureBackend_ErrorsDoNotReflectKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("put", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend down for secret-token")
		b := NewWithClient(failingBlobClient{uploadErr: backendErr}, "container")

		err := b.Put(ctx, "secret-token.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})

		requireError(t, err)
		assertErrorIs(t, err, backendErr)
		assertNotContains(t, err.Error(), "secret-token")
		assertNotContains(t, err.Error(), "backend down")
	})

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend down for secret-token")
		b := NewWithClient(failingBlobClient{downloadErr: backendErr}, "container")

		_, _, err := b.Get(ctx, "secret-token.txt")

		requireError(t, err)
		assertErrorIs(t, err, backendErr)
		assertNotContains(t, err.Error(), "secret-token")
		assertNotContains(t, err.Error(), "backend down")
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend down for secret-token")
		b := NewWithClient(failingBlobClient{deleteErr: backendErr}, "container")

		err := b.Delete(ctx, "secret-token.txt")

		requireError(t, err)
		assertErrorIs(t, err, backendErr)
		assertNotContains(t, err.Error(), "secret-token")
		assertNotContains(t, err.Error(), "backend down")
	})

	t.Run("exists", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend down for secret-token")
		b := NewWithClient(failingBlobClient{downloadErr: backendErr}, "container")

		ok, err := b.Exists(ctx, "secret-token.txt")

		requireError(t, err)
		assertErrorIs(t, err, backendErr)
		if ok {
			t.Fatal("exists returned true on failure")
		}
		assertNotContains(t, err.Error(), "secret-token")
		assertNotContains(t, err.Error(), "backend down")
	})
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("error leaked %q: %s", substr, s)
	}
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v does not wrap target %v", err, target)
	}
}

type stubBlobClient struct{ BlobClient }

type failingBlobClient struct {
	uploadErr   error
	downloadErr error
	deleteErr   error
}

func (c failingBlobClient) UploadStream(context.Context, string, io.Reader, *azblob.UploadStreamOptions) (azblob.UploadStreamResponse, error) {
	return azblob.UploadStreamResponse{}, c.uploadErr
}

func (c failingBlobClient) DownloadStream(context.Context, string, *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error) {
	return azblob.DownloadStreamResponse{}, c.downloadErr
}

func (c failingBlobClient) DeleteBlob(context.Context, string, *azblob.DeleteBlobOptions) (azblob.DeleteBlobResponse, error) {
	return azblob.DeleteBlobResponse{}, c.deleteErr
}

func (c failingBlobClient) NewListBlobsFlatPager(*container.ListBlobsFlatOptions) BlobPager {
	return stubBlobPager{}
}

type stubBlobPager struct{}

func (p stubBlobPager) More() bool { return false }

func (p stubBlobPager) NextPage(context.Context) (container.ListBlobsFlatResponse, error) {
	return container.ListBlobsFlatResponse{}, nil
}
