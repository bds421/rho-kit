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
	// Secret.Version exposes only the bare trailing version segment ("3"),
	// matching the documented secrets.Secret.Version contract ("GCP
	// version number") and the awssm/vaultkv siblings — not the full
	// "projects/.../versions/3" resource path.
	require.Equal(t, "3", s.Version)
}

func TestLoader_VersionIsBareSegment(t *testing.T) {
	// Even when GCP resolves "latest" to a concrete numbered version, the
	// returned resp.Name is a full resource path; gcpsm must surface only
	// the trailing segment.
	api := &fakeAPI{resp: &secretmanagerpb.AccessSecretVersionResponse{
		Name:    "projects/proj-123/secrets/api-key/versions/42",
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("v")},
	}}
	l := gcpsm.New(api, gcpsm.WithProject("proj-123"))
	s, err := l.Get(context.Background(), "api-key")
	require.NoError(t, err)
	require.Equal(t, "42", s.Version)
}

func TestLoader_EmptyResponseNameYieldsEmptyVersion(t *testing.T) {
	// A backend that omits resp.Name must leave Secret.Version empty
	// rather than producing a spurious value.
	api := &fakeAPI{resp: &secretmanagerpb.AccessSecretVersionResponse{
		Payload: &secretmanagerpb.SecretPayload{Data: []byte("v")},
	}}
	l := gcpsm.New(api, gcpsm.WithProject("p"))
	s, err := l.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "", s.Version)
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
