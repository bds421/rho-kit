package gcpsm_test

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/gcpsm/v2"
	"github.com/bds421/rho-kit/infra/secrets/v2"
)

type fakeAPI struct {
	resp *secretmanagerpb.AccessSecretVersionResponse
	err  error
}

func (f *fakeAPI) AccessSecretVersion(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return f.resp, f.err
}

func TestLoader_Get(t *testing.T) {
	api := &fakeAPI{resp: &secretmanagerpb.AccessSecretVersionResponse{
		Name: "projects/p/secrets/k/versions/3",
		Payload: &secretmanagerpb.SecretPayload{
			Data: []byte("hunter2"),
		},
	}}
	l := gcpsm.New(api, gcpsm.WithProject("p"))
	s, err := l.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "hunter2", s.Value.RevealString())
	require.Equal(t, "projects/p/secrets/k/versions/3", s.Version)
}

func TestLoader_NotFound(t *testing.T) {
	api := &fakeAPI{err: status.Error(codes.NotFound, "missing")}
	l := gcpsm.New(api, gcpsm.WithProject("p"))
	_, err := l.Get(context.Background(), "missing")
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

func TestLoader_OtherErrorMapsToUnavailable(t *testing.T) {
	api := &fakeAPI{err: errors.New("PermissionDenied")}
	l := gcpsm.New(api, gcpsm.WithProject("p"))
	_, err := l.Get(context.Background(), "k")
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestLoader_BareNameRequiresProject(t *testing.T) {
	// A bare key against a loader without WithProject must surface as an
	// error on the request path, never a panic — otherwise a background
	// refresh goroutine (CachedLoader.spawnRefresh) would crash the whole
	// process. Matches awssm and vaultkv, which always return errors.
	l := gcpsm.New(&fakeAPI{})
	_, err := l.Get(context.Background(), "k")
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestLoader_FullyQualifiedNameBypassesProject(t *testing.T) {
	api := &fakeAPI{resp: &secretmanagerpb.AccessSecretVersionResponse{
		Name:    "projects/x/secrets/k/versions/1",
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("ok")},
	}}
	l := gcpsm.New(api) // no project
	_, err := l.Get(context.Background(), "projects/x/secrets/k/versions/1")
	require.NoError(t, err)
}

func TestNew_PanicsOnNilAPI(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = gcpsm.New(nil)
}

func TestGet_EmptyPayloadWrapsUnavailable(t *testing.T) {
	api := &fakeAPI{resp: &secretmanagerpb.AccessSecretVersionResponse{
		Name:    "projects/p/secrets/s/versions/1",
		Payload: &secretmanagerpb.SecretPayload{Data: nil},
	}}
	l := gcpsm.New(api, gcpsm.WithProject("p"))
	_, err := l.Get(context.Background(), "s")
	require.Error(t, err)
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestResolveName_StrictProjectRejectsFQPath(t *testing.T) {
	l := gcpsm.New(&fakeAPI{}, gcpsm.WithProject("tenant-a"), gcpsm.WithStrictProject())
	_, err := l.Get(context.Background(), "projects/tenant-b/secrets/db/versions/latest")
	require.Error(t, err)
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}
