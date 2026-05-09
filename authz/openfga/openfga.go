// Package openfga implements [authz.Decider] against an OpenFGA
// store. OpenFGA models authorization as relation tuples (user U has
// relation R to object O) and answers Check(user, relation, object)
// queries in milliseconds at scale.
//
// The kit's [authz.Decider] interface speaks (subject, action,
// resource) triples. The mapping to OpenFGA's vocabulary:
//
//   - subject → User (e.g. "user:alice")
//   - action  → Relation (e.g. "read", "write")
//   - resource → Object (e.g. "doc:1")
//
// The adapter passes these strings through untransformed. Services
// that want richer encoding (e.g. tenant-scoped subjects) should do
// the encoding before calling Allow.
package openfga

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openfga/go-sdk/client"
	"github.com/openfga/go-sdk/credentials"

	"github.com/bds421/rho-kit/authz/v2"
)

// Decider is the OpenFGA-backed [authz.Decider]. Construct via
// [New] with a configured *client.OpenFgaClient.
type Decider struct {
	c       *client.OpenFgaClient
	storeID string
	modelID string
}

// Config bundles the connection knobs the kit takes opinions on.
// StoreID is required; ModelID is recommended in production so the
// service pins to a known model version (otherwise OpenFGA uses the
// store's latest).
//
// FR-037 [MED]: Credentials, HTTPClient, and DefaultHeaders are
// pass-throughs to the OpenFGA SDK so production deployments can
// authenticate (API token, OIDC client credentials), customise the
// outbound HTTP client (timeouts, mTLS, tracing), and inject required
// headers without bypassing the kit wrapper.
type Config struct {
	APIURL         string
	StoreID        string
	ModelID        string
	Credentials    *credentials.Credentials
	HTTPClient     *http.Client
	DefaultHeaders map[string]string
	UserAgent      string
}

// New builds a Decider from cfg. Returns an error if the SDK client
// fails to construct — typically a malformed APIURL.
func New(cfg Config) (*Decider, error) {
	if cfg.APIURL == "" {
		return nil, errors.New("openfga: APIURL must not be empty")
	}
	if cfg.StoreID == "" {
		return nil, errors.New("openfga: StoreID must not be empty")
	}
	c, err := client.NewSdkClient(&client.ClientConfiguration{
		ApiUrl:               cfg.APIURL,
		StoreId:              cfg.StoreID,
		AuthorizationModelId: cfg.ModelID,
		Credentials:          cfg.Credentials,
		HTTPClient:           cfg.HTTPClient,
		DefaultHeaders:       cfg.DefaultHeaders,
		UserAgent:            cfg.UserAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("openfga: build client: %w", err)
	}
	return &Decider{c: c, storeID: cfg.StoreID, modelID: cfg.ModelID}, nil
}

// Allow implements [authz.Decider]. Issues an OpenFGA Check call
// against the configured store/model. Returns nil on Allowed=true,
// [authz.ErrDenied] on Allowed=false, or a wrapped SDK error on
// engine failure.
//
// FR-038 [LOW]: rejects empty subject/action/resource locally
// without making the network call. Empty fields cannot represent a
// real authorization question and would otherwise reach the engine
// as ambiguous or default-allowed checks.
func (d *Decider) Allow(ctx context.Context, subject, action, resource string) error {
	if subject == "" || action == "" || resource == "" {
		return fmt.Errorf("openfga: subject/action/resource must be non-empty (got %q/%q/%q): %w",
			subject, action, resource, authz.ErrDenied)
	}
	body := client.ClientCheckRequest{
		User:     subject,
		Relation: action,
		Object:   resource,
	}
	resp, err := d.c.Check(ctx).Body(body).Execute()
	if err != nil {
		return fmt.Errorf("openfga: check: %w", err)
	}
	if resp == nil || resp.Allowed == nil || !*resp.Allowed {
		return authz.ErrDenied
	}
	return nil
}
