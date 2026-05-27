package awssm

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/bds421/rho-kit/infra/secrets/v2"
)

// API is the minimal aws-sdk-go-v2 secretsmanager surface this backend
// uses. The real [*secretsmanager.Client] satisfies it; tests stub it.
type API interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Loader implements [secrets.Loader] backed by AWS Secrets Manager.
type Loader struct {
	api   API
	stage string // VersionStage filter; default AWSCURRENT
}

// Option configures a [Loader].
type Option func(*Loader)

// WithVersionStage overrides the Secrets Manager VersionStage filter
// (default AWSCURRENT).
func WithVersionStage(stage string) Option {
	if stage == "" {
		panic("awssm: WithVersionStage requires non-empty stage")
	}
	return func(l *Loader) { l.stage = stage }
}

// New wraps the supplied client. Panics on nil client (programmer
// error — the kit's loader contract requires a working client).
func New(api API, opts ...Option) *Loader {
	if api == nil {
		panic("awssm: New requires non-nil API client")
	}
	l := &Loader{api: api, stage: "AWSCURRENT"}
	for _, opt := range opts {
		if opt == nil {
			panic("awssm: option must not be nil")
		}
		opt(l)
	}
	return l
}

// Get resolves the secret. Returns:
//   - (Secret, nil)                          on success
//   - (zero, secrets.ErrSecretNotFound)      when AWS reports ResourceNotFound
//   - (zero, wrapped ErrLoaderUnavailable)   on any transport / auth / quota error
func (l *Loader) Get(ctx context.Context, key string) (secrets.Secret, error) {
	out, err := l.api.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     awsStringPtr(key),
		VersionStage: awsStringPtr(l.stage),
	})
	if err != nil {
		var nf *smtypes.ResourceNotFoundException
		if errors.As(err, &nf) {
			return secrets.Secret{}, secrets.ErrSecretNotFound
		}
		return secrets.Secret{}, fmt.Errorf("awssm: GetSecretValue %s: %w (%v)", key, secrets.ErrLoaderUnavailable, err)
	}
	var raw []byte
	switch {
	case out.SecretString != nil:
		raw = []byte(*out.SecretString)
	case len(out.SecretBinary) > 0:
		raw = append([]byte(nil), out.SecretBinary...)
	default:
		return secrets.Secret{}, fmt.Errorf("awssm: %s has no SecretString or SecretBinary", key)
	}
	version := ""
	if out.VersionId != nil {
		version = *out.VersionId
	}
	return secrets.MakeSecret(raw, version), nil
}

func awsStringPtr(s string) *string { return &s }
