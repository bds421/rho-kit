package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// providerMetadata is the OIDC discovery document subset the kit
// reads. Full RFC 8414 fields are NOT exposed — callers wanting
// non-standard claims should fetch the document themselves.
type providerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	ResponseTypes         []string `json:"response_types_supported"`
	IDTokenSigningAlgs    []string `json:"id_token_signing_alg_values_supported"`
}

// discoverProvider fetches /.well-known/openid-configuration from
// issuer + verifies the document's issuer field matches (RFC 8414
// §3.3 requires identity).
func discoverProvider(ctx context.Context, hc *http.Client, issuer string) (providerMetadata, error) {
	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("%w: build request: %v", ErrIssuerDiscovery, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("%w: fetch: %v", ErrIssuerDiscovery, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return providerMetadata{}, fmt.Errorf("%w: HTTP %d", ErrIssuerDiscovery, resp.StatusCode)
	}
	var meta providerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return providerMetadata{}, fmt.Errorf("%w: decode: %v", ErrIssuerDiscovery, err)
	}
	if meta.Issuer == "" {
		return providerMetadata{}, fmt.Errorf("%w: missing issuer field", ErrIssuerDiscovery)
	}
	// RFC 8414 §3.3: discovered issuer MUST match the discovery URL's
	// prefix (sans trailing slash). Prevents a malicious redirect at
	// .well-known from impersonating a different issuer.
	if strings.TrimRight(meta.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return providerMetadata{}, fmt.Errorf("%w: issuer %q doesn't match config %q",
			ErrIssuerDiscovery, meta.Issuer, issuer)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return providerMetadata{}, errors.New("oauth2: discovery missing authorization_endpoint or token_endpoint")
	}
	return meta, nil
}
