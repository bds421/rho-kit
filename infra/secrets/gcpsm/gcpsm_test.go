package gcpsm_test

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
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

func (f *fakeAPI) AccessSecretVersion(_ context.Context, _ *secretmanagerpb.AccessSecretVersionRequest, _ ...option.ClientOption) (*secretmanagerpb.AccessSecretVersionResponse, error) {
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
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when bare name and no project")
		}
	}()
	l := gcpsm.New(&fakeAPI{})
	_, _ = l.Get(context.Background(), "k")
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
