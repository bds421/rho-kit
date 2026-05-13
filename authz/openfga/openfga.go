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
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openfga/go-sdk/client"
	"github.com/openfga/go-sdk/credentials"

	"github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

const (
	defaultHTTPClientTimeout      = 10 * time.Second
	defaultMaxIdleConnsPerHost    = 100
	defaultMaxResponseHeaderBytes = 64 * 1024
	minimumHTTPClientTLSVersion   = tls.VersionTLS12
)

// ErrRedirectBlocked is returned by OpenFGA HTTP clients without an explicit
// redirect policy when the API attempts to redirect. Authorization checks
// should go only to the configured policy engine endpoint unless callers
// deliberately install a custom redirect policy.
var ErrRedirectBlocked = errors.New("openfga: redirects are disabled by default")

// Decider is the OpenFGA-backed [authz.Decider]. Construct via
// [New] with a configured *client.OpenFgaClient.
//
// Safe for concurrent use — all state is immutable post-construction;
// the embedded OpenFGA client is itself goroutine-safe.
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
// authenticate (API token, OIDC client credentials), customise the outbound
// HTTP client (timeouts, mTLS, tracing), and inject required headers without
// bypassing the kit wrapper. Custom HTTP clients with no redirect policy are
// shallow-copied and hardened to block redirects.
type Config struct {
	APIURL         string
	StoreID        string
	ModelID        string
	Credentials    *credentials.Credentials
	HTTPClient     *http.Client
	DefaultHeaders map[string]string
	UserAgent      string

	// AllowInsecureHTTP permits http:// APIURL values. Leave false in
	// production: OpenFGA checks carry authorization subjects,
	// resources, and often bearer/OIDC credentials. Plaintext is
	// acceptable only for local development, test containers, or a
	// separately authenticated private link that the service owner has
	// explicitly reviewed.
	AllowInsecureHTTP bool
}

// LogValue implements slog.LogValuer to prevent accidental logging of
// OpenFGA SDK credentials and caller-provided authentication headers.
func (c Config) LogValue() slog.Value {
	apiURLValid, apiHostConfigured := openFGAURLLogState(c.APIURL)
	return slog.GroupValue(
		slog.Bool("api_url_configured", c.APIURL != ""),
		slog.Bool("api_url_valid", apiURLValid),
		slog.Bool("api_host_configured", apiHostConfigured),
		slog.Bool("store_id_configured", c.StoreID != ""),
		slog.Bool("model_id_configured", c.ModelID != ""),
		slog.Bool("credentials_configured", c.Credentials != nil),
		slog.Bool("http_client_configured", c.HTTPClient != nil),
		slog.Bool("default_headers_configured", len(c.DefaultHeaders) > 0),
		slog.Bool("user_agent_configured", c.UserAgent != ""),
		slog.Bool("allow_insecure_http", c.AllowInsecureHTTP),
	)
}

func openFGAURLLogState(rawURL string) (valid, hostConfigured bool) {
	if rawURL == "" {
		return true, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, false
	}
	return true, u.Host != ""
}

// New builds a Decider from cfg. Returns an error if the SDK client
// fails to construct — typically a malformed APIURL.
func New(cfg Config) (*Decider, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	c, err := client.NewSdkClient(clientConfiguration(cfg))
	if err != nil {
		return nil, fmt.Errorf("openfga: build client: %w", err)
	}
	return &Decider{c: c, storeID: cfg.StoreID, modelID: cfg.ModelID}, nil
}

func validateConfig(cfg Config) error {
	if cfg.APIURL == "" {
		return errors.New("openfga: APIURL must not be empty")
	}
	u, err := url.Parse(cfg.APIURL)
	if err != nil {
		return errors.New("openfga: APIURL is invalid")
	}
	if u.Scheme == "" || u.Host == "" {
		return errors.New("openfga: APIURL must be an absolute http(s) URL")
	}
	if u.User != nil {
		return errors.New("openfga: APIURL must not include userinfo credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("openfga: APIURL must not include query or fragment components")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !cfg.AllowInsecureHTTP {
			return errors.New("openfga: APIURL must use https unless AllowInsecureHTTP is set")
		}
	default:
		return errors.New("openfga: APIURL scheme is not supported")
	}
	if cfg.StoreID == "" {
		return errors.New("openfga: StoreID must not be empty")
	}
	for name, value := range cfg.DefaultHeaders {
		if !validHeaderFieldName(name) {
			return errors.New("openfga: DefaultHeaders contains invalid header name")
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("openfga: DefaultHeaders contains a control character")
		}
	}
	return nil
}

func clientConfiguration(cfg Config) *client.ClientConfiguration {
	httpClient := openFGAHTTPClient(cfg.HTTPClient)
	return &client.ClientConfiguration{
		ApiUrl:               cfg.APIURL,
		StoreId:              cfg.StoreID,
		AuthorizationModelId: cfg.ModelID,
		Credentials:          cfg.Credentials,
		HTTPClient:           httpClient,
		DefaultHeaders:       cloneHeaders(cfg.DefaultHeaders),
		UserAgent:            cfg.UserAgent,
	}
}

func openFGAHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient()
	}
	if client.Timeout > 0 && client.Transport != nil && client.CheckRedirect != nil {
		return client
	}
	cloned := *client
	if cloned.Timeout <= 0 {
		cloned.Timeout = defaultHTTPClientTimeout
	}
	if cloned.Transport == nil {
		transport := cloneDefaultTransport()
		transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
		transport.MaxResponseHeaderBytes = defaultMaxResponseHeaderBytes
		transport.TLSClientConfig = cloneTLSConfigWithFloor(transport.TLSClientConfig)
		cloned.Transport = transport
	}
	if cloned.CheckRedirect == nil {
		cloned.CheckRedirect = blockRedirect
	}
	return &cloned
}

func defaultHTTPClient() *http.Client {
	transport := cloneDefaultTransport()
	transport.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	transport.MaxResponseHeaderBytes = defaultMaxResponseHeaderBytes
	transport.TLSClientConfig = cloneTLSConfigWithFloor(transport.TLSClientConfig)
	return &http.Client{
		Timeout:       defaultHTTPClientTimeout,
		Transport:     transport,
		CheckRedirect: blockRedirect,
	}
}

func cloneDefaultTransport() *http.Transport {
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		return tr.Clone()
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func cloneTLSConfigWithFloor(cfg *tls.Config) *tls.Config {
	cloned, err := tlsclone.ConfigOrEmptyWithFloor(cfg, minimumHTTPClientTLSVersion)
	if err != nil {
		if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
			panic("openfga: HTTP client TLS InsecureSkipVerify=true is not permitted")
		}
		panic("openfga: default HTTP client TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned
}

func blockRedirect(_ *http.Request, _ []*http.Request) error {
	return ErrRedirectBlocked
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = v
	}
	return out
}

func validHeaderFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z':
		case 'A' <= c && c <= 'Z':
		case '0' <= c && c <= '9':
		case c == '!' || c == '#' || c == '$' || c == '%' || c == '&' || c == '\'' || c == '*':
		case c == '+' || c == '-' || c == '.' || c == '^' || c == '_' || c == '`' || c == '|' || c == '~':
		default:
			return false
		}
	}
	return true
}

func (d *Decider) ready() error {
	if d == nil || d.c == nil {
		return errors.New("openfga: decider is not initialized")
	}
	return nil
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
	if err := d.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("openfga: context must not be nil")
	}
	if err := authz.ValidateRequest(authz.Request{Subject: subject, Action: action, Resource: resource}); err != nil {
		return fmt.Errorf("openfga: invalid check request: %w", err)
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
