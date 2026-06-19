package awssm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/awssm/v2"
	"github.com/bds421/rho-kit/infra/secrets/v2"
)

type fakeAPI struct {
	out *secretsmanager.GetSecretValueOutput
	err error
}

func (f *fakeAPI) GetSecretValue(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	return f.out, f.err
}

func strPtr(s string) *string { return &s }

func TestLoader_GetReturnsSecretString(t *testing.T) {
	api := &fakeAPI{out: &secretsmanager.GetSecretValueOutput{
		SecretString: strPtr("hunter2"),
		VersionId:    strPtr("abc"),
	}}
	l := awssm.New(api)
	s, err := l.Get(context.Background(), "prod/api/key")
	require.NoError(t, err)
	require.Equal(t, "hunter2", s.Value.RevealString())
	require.Equal(t, "abc", s.Version)
}

func TestLoader_GetReturnsSecretBinary(t *testing.T) {
	api := &fakeAPI{out: &secretsmanager.GetSecretValueOutput{
		SecretBinary: []byte{0x01, 0x02, 0x03},
		VersionId:    strPtr("v1"),
	}}
	l := awssm.New(api)
	s, err := l.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, []byte{0x01, 0x02, 0x03}, s.Value.Reveal())
}

func TestLoader_NotFoundMapsToSentinel(t *testing.T) {
	api := &fakeAPI{err: &smtypes.ResourceNotFoundException{}}
	l := awssm.New(api)
	_, err := l.Get(context.Background(), "missing")
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

func TestLoader_OtherErrorMapsToUnavailable(t *testing.T) {
	api := &fakeAPI{err: errors.New("AccessDeniedException: not authorised")}
	l := awssm.New(api)
	_, err := l.Get(context.Background(), "k")
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestLoader_NoPayloadMapsToUnavailable(t *testing.T) {
	api := &fakeAPI{out: &secretsmanager.GetSecretValueOutput{
		VersionId: strPtr("v1"),
	}}
	l := awssm.New(api)
	_, err := l.Get(context.Background(), "k")
	require.Error(t, err)
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestNew_PanicsOnNilAPI(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil api")
		}
	}()
	_ = awssm.New(nil)
}

func TestWithVersionStage_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on empty stage")
		}
	}()
	_ = awssm.WithVersionStage("")
}
