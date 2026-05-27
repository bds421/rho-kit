package vaultkv_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/v2"
	"github.com/bds421/rho-kit/infra/secrets/vaultkv/v2"
)

type fakeAPI struct {
	resp *api.KVSecret
	err  error
}

func (f *fakeAPI) Get(_ context.Context, _ string) (*api.KVSecret, error) {
	return f.resp, f.err
}

func TestLoader_GetValueField(t *testing.T) {
	md := fakeMetadata(7)
	api := &fakeAPI{resp: &api.KVSecret{
		Data:            map[string]any{"value": "hunter2"},
		VersionMetadata: &md,
	}}
	l := vaultkv.New(api)
	s, err := l.Get(context.Background(), "kv/data/k")
	require.NoError(t, err)
	require.Equal(t, "hunter2", s.Value.RevealString())
	require.Equal(t, "7", s.Version)
}

func TestLoader_CustomField(t *testing.T) {
	api := &fakeAPI{resp: &api.KVSecret{
		Data: map[string]any{"password": "p", "value": "ignore"},
	}}
	l := vaultkv.New(api, vaultkv.WithField("password"))
	s, err := l.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "p", s.Value.RevealString())
}

func TestLoader_NotFoundFromResponseError(t *testing.T) {
	rerr := fakeAPIResponseError(404)
	api := &fakeAPI{err: &rerr}
	l := vaultkv.New(api)
	_, err := l.Get(context.Background(), "missing")
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

func TestLoader_NotFoundFromNilData(t *testing.T) {
	api := &fakeAPI{resp: &api.KVSecret{Data: nil}}
	l := vaultkv.New(api)
	_, err := l.Get(context.Background(), "k")
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

func TestLoader_MissingFieldErrors(t *testing.T) {
	api := &fakeAPI{resp: &api.KVSecret{Data: map[string]any{"other": "x"}}}
	l := vaultkv.New(api)
	_, err := l.Get(context.Background(), "k")
	require.Error(t, err)
}

func TestLoader_FieldTypeError(t *testing.T) {
	api := &fakeAPI{resp: &api.KVSecret{Data: map[string]any{"value": 42}}}
	l := vaultkv.New(api)
	_, err := l.Get(context.Background(), "k")
	require.Error(t, err)
}

func TestLoader_OtherErrorMapsToUnavailable(t *testing.T) {
	api := &fakeAPI{err: errors.New("network down")}
	l := vaultkv.New(api)
	_, err := l.Get(context.Background(), "k")
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestNew_PanicsOnNilAPI(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = vaultkv.New(nil)
}

// fakeAPIResponseError is needed because api.ResponseError uses
// unexported fields we can't construct as a literal. We synthesize via
// a helper that returns the actual type so errors.As works.
type apiRespErr = api.ResponseError

func fakeAPIResponseError(code int) apiRespErr {
	return apiRespErr{StatusCode: code, Errors: []string{"not found"}}
}

// fakeMetadata is a tiny helper to construct VersionMetadata since the
// type's fields aren't all exported in every vault SDK version.
func fakeMetadata(v int) api.KVVersionMetadata {
	return api.KVVersionMetadata{Version: v}
}
