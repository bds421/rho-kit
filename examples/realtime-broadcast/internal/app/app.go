// Package app wires the realtime-broadcast EXAMPLE.
//
// Composition shown:
//
//	httpx.NewServer
//	  ↳ /connection/websocket  → centrifuge.Node.WebsocketHandler
//	                              (centrifuge.NewNode wired with
//	                               WithJWTAuth(verifier) +
//	                               WithChannelClassifier(...))
//	  ↳ /demo/token            → debug endpoint that signs a JWT
//	  ↳ /demo/jwks             → public-key JWKS the verifier reads
//
// The kit's contributions are:
//   - centrifuge.NewNode wraps the centrifuge library and exposes
//     it as a lifecycle.Component (Start/Stop) so it composes with
//     runtime/lifecycle.Runner alongside the HTTP server.
//   - WithJWTAuth wires connect-time JWT verification via the kit's
//     jwtutil.Provider — issuer, audience, signature, expiry are
//     all validated. kit-doctor's `centrifuge-missing-jwt-auth`
//     rule flags any centrifuge.NewNode call that omits this.
//   - WithChannelClassifier projects channel names through a
//     bounded-cardinality "class" label (user / room / system)
//     so per-tenant channel names cannot inflate Prometheus
//     cardinality.
//
// SECURITY: this example collapses issuer + verifier into one
// binary so the smoke test stands up without an external IDP.
// Production deployments:
//   - load the JWKS from a real issuer (Auth0 / Cognito /
//     Keycloak / custom) via `jwtutil.NewProvider(jwksURL, ...)`;
//   - never expose the signing private key on the broadcast
//     server;
//   - run the centrifuge node behind TLS termination matching
//     the issuer's expected audience (e.g. wss://realtime.example.com).
package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/bds421/rho-kit/app/v2"
	apphttp "github.com/bds421/rho-kit/app/http/v2"
	"github.com/bds421/rho-kit/realtime/centrifuge/v2"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

const (
	demoIssuer    = "https://demo-issuer.example.com"
	demoAudience  = "https://realtime.example.com"
	demoKeyID     = "demo-kid"
	defaultJWTTTL = 5 * time.Minute
	tokenPath     = "/demo/token"
	jwksPath      = "/demo/jwks"
	websocketPath = "/connection/websocket"
)

// Run boots the realtime-broadcast service via app.Builder.
// The centrifuge Node is a lifecycle.Component, so it composes
// alongside the HTTP server through Builder.With for the
// centrifuge module and Router for the websocket handler.
//
// Builder runs the always-on validator at startup; the example
// opts out per-policy (apphttp.WithoutTLS, WithoutRateLimit) for
// curl/test convenience. kit-doctor flags each opt-out in
// production code.
func Run(ctx context.Context) error {
	logger := slog.Default()
	signing, err := newSigningKey()
	if err != nil {
		return fmt.Errorf("generate signing key: %w", err)
	}
	verifier, err := signing.newVerifier()
	if err != nil {
		return fmt.Errorf("build jwt verifier: %w", err)
	}
	node, err := centrifuge.NewNode(
		centrifuge.WithJWTAuth(verifier),
		centrifuge.WithChannelClassifier(classifyChannel),
		centrifuge.WithLogger(logger),
	)
	if err != nil {
		return fmt.Errorf("build centrifuge node: %w", err)
	}

	cfg := app.BaseConfig{
		Server:      app.ServerConfig{Host: "127.0.0.1", Port: 8096},
		Internal:    app.InternalConfig{Host: "127.0.0.1", Port: 9096},
		Environment: "example",
		LogLevel:    "info",
	}
	return app.New("realtime-broadcast", "0.0.0-example", cfg).
		Logger(logger).
		WithoutRateLimit().
		// Example listens on plain http for curl/test convenience.
		// kit-doctor:allow apphttp-without-tls
		With(apphttp.Module(apphttp.WithoutTLS())).
		// Centrifuge.Node is a lifecycle.Component; register it
		// via Background so Builder coordinates Start/Stop with
		// the HTTP server. Builder enforces graceful shutdown
		// ordering: HTTP server stops first (no new connections),
		// then centrifuge drains.
		Background("centrifuge", func(bgCtx context.Context) error {
			return node.Start(bgCtx)
		}).
		OnShutdown(func(shutdownCtx context.Context) {
			_ = node.Stop(shutdownCtx)
		}).
		Router(func(_ app.Infrastructure) http.Handler {
			mux := http.NewServeMux()
			mux.Handle("POST "+websocketPath, node.WebsocketHandler())
			mux.Handle("GET "+websocketPath, node.WebsocketHandler())
			mux.HandleFunc("GET "+tokenPath, signing.handleTokenIssue)
			mux.HandleFunc("GET "+jwksPath, signing.handleJWKSExpose)
			return mux
		}).
		RunContext(ctx)
}

// Suppress an "imported and not used" warning when the example's
// own helpers are stripped during refactor. lifecycle is still
// referenced in tests + the documentation comment.
var _ = lifecycle.NewRunner

// classifyChannel maps centrifuge channel names to bounded
// cardinality "class" labels. The kit projects this through
// promutil.OpaqueLabelValue as a safety net, but a well-behaved
// classifier returns a fixed enum like the one below.
func classifyChannel(channel string) string {
	switch {
	case strings.HasPrefix(channel, "user:"):
		return "user"
	case strings.HasPrefix(channel, "room:"):
		return "room"
	case strings.HasPrefix(channel, "system:"):
		return "system"
	default:
		return "default"
	}
}

// signingKey bundles the example's ECDSA keypair with the JWKS
// representation the verifier reads. In production this lives
// behind the issuer; the broadcast server holds only the public
// half.
type signingKey struct {
	priv     *ecdsa.PrivateKey
	jwksJSON []byte
}

func newSigningKey() (*signingKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	pubJWK, err := jwk.Import(priv.PublicKey)
	if err != nil {
		return nil, err
	}
	_ = pubJWK.Set(jwk.KeyIDKey, demoKeyID)
	_ = pubJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = pubJWK.Set(jwk.KeyUsageKey, "sig")
	set := jwk.NewSet()
	if err := set.AddKey(pubJWK); err != nil {
		return nil, err
	}
	jwksJSON, err := json.Marshal(set)
	if err != nil {
		return nil, err
	}
	return &signingKey{priv: priv, jwksJSON: jwksJSON}, nil
}

func (s *signingKey) newVerifier() (*jwtutil.Provider, error) {
	ks, err := jwtutil.ParseKeySet(s.jwksJSON)
	if err != nil {
		return nil, err
	}
	return jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithExpectedIssuer(demoIssuer),
		jwtutil.WithExpectedAudience(demoAudience),
	), nil
}

// SignToken issues a JWT signed with the demo key. Exposed for
// the smoke test and the /demo/token endpoint.
func (s *signingKey) SignToken(sub string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = defaultJWTTTL
	}
	tok, err := jwt.NewBuilder().
		Issuer(demoIssuer).
		Audience([]string{demoAudience}).
		Subject(sub).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(ttl)).
		Build()
	if err != nil {
		return "", err
	}
	jwkKey, err := jwk.Import(s.priv)
	if err != nil {
		return "", err
	}
	_ = jwkKey.Set(jwk.KeyIDKey, demoKeyID)
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func (s *signingKey) handleTokenIssue(w http.ResponseWriter, r *http.Request) {
	sub := r.URL.Query().Get("sub")
	if sub == "" {
		sub = "demo-user"
	}
	tok, err := s.SignToken(sub, 0)
	if err != nil {
		http.Error(w, "sign failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"token":      tok,
		"connect_to": websocketPath,
	})
}

func (s *signingKey) handleJWKSExpose(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.jwksJSON)
}
