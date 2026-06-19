package gcpsm

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/secrets/v2"
)

// API is the minimal GCP Secret Manager surface this backend uses. The
// real [*secretmanager.Client] satisfies it; tests stub it.
type API interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest, opts ...gax.CallOption) (*secretmanagerpb.AccessSecretVersionResponse, error)
}

// Loader implements [secrets.Loader] backed by GCP Secret Manager.
type Loader struct {
	api     API
	project string
	version string // "latest" or numeric/alias
}

// Option configures a [Loader].
type Option func(*Loader)

// WithProject pins the GCP project ID. Required if the secret key
// passed to Get is bare ("my-secret" rather than a full
// "projects/PROJECT/secrets/NAME/versions/V" path).
func WithProject(id string) Option {
	if id == "" {
		panic("gcpsm: WithProject requires non-empty project id")
	}
	return func(l *Loader) { l.project = id }
}

// WithVersion overrides the default version "latest".
func WithVersion(v string) Option {
	if v == "" {
		panic("gcpsm: WithVersion requires non-empty version")
	}
	return func(l *Loader) { l.version = v }
}

// New wraps the supplied GCP client. Panics on nil client.
func New(api API, opts ...Option) *Loader {
	if api == nil {
		panic("gcpsm: New requires non-nil API client")
	}
	l := &Loader{api: api, version: "latest"}
	for _, opt := range opts {
		if opt == nil {
			panic("gcpsm: option must not be nil")
		}
		opt(l)
	}
	return l
}

// Get resolves the secret. Returns:
//   - (Secret, nil)                          on success
//   - (zero, secrets.ErrSecretNotFound)      when GCP reports NotFound
//   - (zero, wrapped ErrLoaderUnavailable)   on transport / auth errors
func (l *Loader) Get(ctx context.Context, key string) (secrets.Secret, error) {
	name, err := l.resolveName(key)
	if err != nil {
		return secrets.Secret{}, err
	}
	resp, err := l.api.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return secrets.Secret{}, secrets.ErrSecretNotFound
		}
		return secrets.Secret{}, redact.WrapSentinel(secrets.ErrLoaderUnavailable,
			redact.WrapError("gcpsm: AccessSecretVersion "+key, err))
	}
	if resp.Payload == nil || len(resp.Payload.Data) == 0 {
		return secrets.Secret{}, errors.New("gcpsm: empty payload")
	}
	version := ""
	if resp.Name != "" {
		// Name is the full resource path
		// "projects/P/secrets/S/versions/N"; Secret.Version exposes it
		// verbatim. Unlike awssm (bare VersionId) and vaultkv (bare
		// integer), gcpsm returns the full path — see gcpsm_test.go,
		// which pins this behavior.
		version = resp.Name
	}
	return secrets.MakeSecret(append([]byte(nil), resp.Payload.Data...), version), nil
}

func (l *Loader) resolveName(key string) (string, error) {
	// If the caller passes a fully-qualified path, use it verbatim so
	// they can target a different project / secret-version per call.
	if hasPrefix(key, "projects/") {
		return key, nil
	}
	if l.project == "" {
		// A bare key against a loader without WithProject is a
		// configuration error. Return it (rather than panicking) so a
		// per-call/per-tenant key on a misconfigured loader surfaces as
		// an error on the request path instead of crashing the caller's
		// goroutine — matching awssm and vaultkv.
		return "", redact.WrapSentinel(secrets.ErrLoaderUnavailable,
			redact.WrapError("gcpsm: AccessSecretVersion "+key,
				errors.New("bare secret name requires WithProject")))
	}
	return fmt.Sprintf("projects/%s/secrets/%s/versions/%s", l.project, key, l.version), nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
