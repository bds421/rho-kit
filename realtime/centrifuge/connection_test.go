package centrifuge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
	cfg "github.com/centrifugal/centrifuge"
	"github.com/coder/websocket"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
)

const fixtureSubject = "11111111-1111-4111-8111-111111111111"

type bindingContextKey struct{}

func fixtureTokenProvider(t *testing.T) (*jwtutil.Provider, func(string) string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	set, err := jwtutil.ParseKeySetFromPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}), "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	provider := jwtutil.NewProviderWithKeySet(set, jwtutil.WithExpectedIssuer("https://issuer.invalid"), jwtutil.WithExpectedAudience("fixture-realtime"), jwtutil.WithStringClaims("sid"))
	return provider, func(subject string) string {
		payload, err := json.Marshal(map[string]any{"iss": "https://issuer.invalid", "aud": "fixture-realtime", "sub": subject, "sid": "fixture-session", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		headers := jws.NewHeaders()
		if err = headers.Set(jws.KeyIDKey, "fixture-key"); err != nil {
			t.Fatal(err)
		}
		token, err := jws.Sign(payload, jws.WithKey(jwa.ES256(), key, jws.WithProtectedHeaders(headers)))
		if err != nil {
			t.Fatal(err)
		}
		return string(token)
	}
}

func fixtureSocket(t *testing.T, node *Node) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	if err := node.Underlying().Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := node.Underlying().Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(node.WebsocketHandler())
	t.Cleanup(server.Close)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, ctx
}

func socketCommand(t *testing.T, ctx context.Context, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

type socketReply struct {
	ID    int `json:"id"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func readSocketReply(t *testing.T, ctx context.Context, conn *websocket.Conn, id int) socketReply {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var reply socketReply
			if err = decoder.Decode(&reply); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if reply.ID == id {
				return reply
			}
		}
	}
}

func TestVerifiedConnectionPoliciesPreserveJWTIdentityAndDefaultChannelDenial(t *testing.T) {
	provider, sign := fixtureTokenProvider(t)
	connected := make(chan Connection, 1)
	disconnected := make(chan cfg.DisconnectEvent, 1)
	var refreshed atomic.Int64
	node, err := NewNode(WithJWTAuth(provider), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithVerifiedConnectPolicy(func(ctx context.Context, e cfg.ConnectEvent, claims jwtutil.Claims) (ConnectionPolicyReply, error) {
			if sid, ok := claims.StringClaim("sid"); !ok || sid != "fixture-session" {
				return ConnectionPolicyReply{}, errors.New("wrong session")
			}
			claims.Subject = "attempted-identity-change"
			return ConnectionPolicyReply{Context: context.WithValue(ctx, bindingContextKey{}, "verified-session"), ExpireAt: time.Now().Add(time.Minute).Unix()}, nil
		}), WithVerifiedRefreshPolicy(func(ctx context.Context, client Connection, e cfg.RefreshEvent, claims jwtutil.Claims) (ConnectionPolicyReply, error) {
			if ctx.Value(bindingContextKey{}) != "verified-session" || client.UserID() != fixtureSubject || claims.Subject != fixtureSubject {
				return ConnectionPolicyReply{}, errors.New("binding mismatch")
			}
			refreshed.Add(1)
			return ConnectionPolicyReply{ExpireAt: time.Now().Add(time.Minute).Unix()}, nil
		}), WithConnectObserver(func(client Connection) { connected <- client }), WithDisconnectObserver(func(_ Connection, event cfg.DisconnectEvent) { disconnected <- event }))
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, node)
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{"token": sign(fixtureSubject)}})
	if reply := readSocketReply(t, ctx, conn, 1); reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var client Connection
	select {
	case client = <-connected:
	case <-ctx.Done():
		t.Fatal("connect observer missing")
	}
	if client.UserID() != fixtureSubject || client.Context().Value(bindingContextKey{}) != "verified-session" {
		t.Fatal("verified identity/context lost")
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 2, "subscribe": map[string]any{"channel": "private:fixture"}})
	if reply := readSocketReply(t, ctx, conn, 2); reply.Error == nil || reply.Error.Code != int(cfg.ErrorPermissionDenied.Code) {
		t.Fatal("kit default channel denial replaced")
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 3, "refresh": map[string]any{"token": sign(fixtureSubject)}})
	if reply := readSocketReply(t, ctx, conn, 3); reply.Error != nil || refreshed.Load() != 1 {
		t.Fatal("verified refresh not invoked")
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 4, "refresh": map[string]any{"token": sign("different-subject")}})
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("identity-changing refresh did not close the transport")
	}
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("identity-changing refresh did not disconnect")
	}
	if refreshed.Load() != 1 {
		t.Fatal("refresh policy ran before subject verification")
	}
}

func TestVerifiedConnectPolicyIsNotCalledForInvalidJWT(t *testing.T) {
	provider, _ := fixtureTokenProvider(t)
	var invoked atomic.Int64
	node, err := NewNode(WithJWTAuth(provider), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithVerifiedConnectPolicy(func(context.Context, cfg.ConnectEvent, jwtutil.Claims) (ConnectionPolicyReply, error) {
		invoked.Add(1)
		return ConnectionPolicyReply{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, node)
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{"token": "invalid-token"}})
	_, _, _ = conn.Read(ctx)
	if invoked.Load() != 0 {
		t.Fatal("application policy bypassed mandatory JWT verification")
	}
}

func TestConnectionPolicyCannotExtendOrDisableJWTExpiry(t *testing.T) {
	now := time.Now()
	base := now.Add(time.Minute).Unix()
	if expiry, err := policyExpiry(base, 0, now); err != nil || expiry != base {
		t.Fatal("zero policy expiry removed token expiry")
	}
	if _, err := policyExpiry(base, base+1, now); err == nil {
		t.Fatal("policy extended token expiry")
	}
	if _, err := policyExpiry(base, now.Unix(), now); err == nil {
		t.Fatal("expired policy allowed")
	}
	if expiry, err := policyExpiry(base, base-10, now); err != nil || expiry != base-10 {
		t.Fatal("earlier expiry not applied")
	}
}

func TestConnectionPolicyRespectsParentDeadlineAndCancellation(t *testing.T) {
	now := time.Now()
	deadline := now.Add(20 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	expiry, err := boundedPolicyExpiry(ctx, now.Add(time.Hour).Unix(), ConnectionPolicyReply{Context: context.Background()}, now)
	if err != nil || expiry != deadline.Unix() {
		t.Fatal("policy context removed incoming deadline")
	}
	cancel()
	if _, err = boundedPolicyExpiry(ctx, now.Add(time.Hour).Unix(), ConnectionPolicyReply{}, now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled parent accepted")
	}
}

func TestVerifiedConnectionPoliciesRequireJWTAndRejectNilCallbacks(t *testing.T) {
	_, err := NewNode(WithAnonymousConnectionsUnsafe(), WithVerifiedConnectPolicy(func(context.Context, cfg.ConnectEvent, jwtutil.Claims) (ConnectionPolicyReply, error) {
		return ConnectionPolicyReply{}, nil
	}))
	if err == nil {
		t.Fatal("verified policy used on anonymous node")
	}
	for _, fn := range []func(){func() { WithVerifiedConnectPolicy(nil) }, func() { WithVerifiedRefreshPolicy(nil) }, func() { WithConnectObserver(nil) }, func() { WithDisconnectObserver(nil) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("nil callback accepted")
				}
			}()
			fn()
		}()
	}
}
