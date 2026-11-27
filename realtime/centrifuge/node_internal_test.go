package centrifuge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	cfg "github.com/centrifugal/centrifuge"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

func TestAuthorizeChannel_DefaultDeny(t *testing.T) {
	n := &Node{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	err := n.authorizeChannel(nil, "user:1", nil)
	if !errors.Is(err, cfg.ErrorPermissionDenied) {
		t.Fatalf("default deny: got %v, want ErrorPermissionDenied", err)
	}
}

func TestAuthorizeChannel_OpenChannelsUnsafe(t *testing.T) {
	n := &Node{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		openChannelsUnsafe: true,
	}
	if err := n.authorizeChannel(nil, "user:1", nil); err != nil {
		t.Fatalf("open channels: got %v, want nil", err)
	}
}

func TestAuthorizeChannel_AuthorizerAllow(t *testing.T) {
	n := &Node{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	auth := func(_ context.Context, e ChannelAuthEvent) error {
		if e.Channel != "room:a" {
			t.Fatalf("unexpected channel %q", e.Channel)
		}
		return nil
	}
	if err := n.authorizeChannel(nil, "room:a", auth); err != nil {
		t.Fatalf("authorizer allow: got %v, want nil", err)
	}
}

func TestAuthorizeChannel_AuthorizerDenyMapsToPermissionDenied(t *testing.T) {
	n := &Node{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	auth := func(context.Context, ChannelAuthEvent) error {
		return errors.New("not your channel")
	}
	err := n.authorizeChannel(nil, "user:other", auth)
	if !errors.Is(err, cfg.ErrorPermissionDenied) {
		t.Fatalf("authorizer deny: got %v, want ErrorPermissionDenied", err)
	}
}

func TestWithOpenChannelsUnsafe_AndAuthorizerOptions(t *testing.T) {
	node, err := NewNode(
		WithAnonymousConnectionsUnsafe(),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithOpenChannelsUnsafe(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !node.openChannelsUnsafe {
		t.Fatal("expected openChannelsUnsafe")
	}

	allow := func(context.Context, ChannelAuthEvent) error { return nil }
	node2, err := NewNode(
		WithAnonymousConnectionsUnsafe(),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSubscribeAuthorizer(allow),
		WithPublishAuthorizer(allow),
	)
	if err != nil {
		t.Fatal(err)
	}
	if node2.subscribeAuthorizer == nil || node2.publishAuthorizer == nil {
		t.Fatal("expected authorizers to be set")
	}
}

func TestDisconnectReason_Classifies(t *testing.T) {
	cases := []struct {
		name string
		d    cfg.Disconnect
		want string
	}{
		{
			name: "client-initiated close is clean",
			d:    cfg.DisconnectConnectionClosed,
			want: disconnectReasonClean,
		},
		{
			name: "server shutdown is stale",
			d:    cfg.DisconnectShutdown,
			want: disconnectReasonStale,
		},
		{
			name: "server invalid-token disconnect is stale",
			d:    cfg.DisconnectInvalidToken,
			want: disconnectReasonStale,
		},
		{
			name: "custom application disconnect code is stale",
			d:    cfg.Disconnect{Code: 4001, Reason: "kicked"},
			want: disconnectReasonStale,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := disconnectReason(tc.d)
			if got != tc.want {
				t.Fatalf("disconnectReason(%+v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestValueToString_RendersScalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"error", errors.New("boom"), "boom"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"uint64", uint64(9000), "9000"},
		{"float64", 3.5, "3.5"},
		{"nil", nil, ""},
		{"fallback struct", struct{ A int }{A: 1}, "{1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := valueToString(tc.in)
			if got != tc.want {
				t.Fatalf("valueToString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConnectVerifyFailure(t *testing.T) {
	cases := []struct {
		err     error
		code    uint32
		outcome string
	}{
		// Infrastructure: JWKS not ready / stale → temporary server error.
		{jwtutil.ErrKeySetUnavailable, cfg.DisconnectServerError.Code, connectOutcomeError},
		{jwtutil.ErrKeySetNotReady, cfg.DisconnectServerError.Code, connectOutcomeError},
		{jwtutil.ErrKeySetStale, cfg.DisconnectServerError.Code, connectOutcomeError},
		{fmt.Errorf("wrap: %w", jwtutil.ErrKeySetNotReady), cfg.DisconnectServerError.Code, connectOutcomeError},
		// Expired → token expired, on which the client fetches a new token.
		{fmt.Errorf("%w: exp not satisfied", jwtutil.ErrTokenExpired), cfg.ErrorTokenExpired.Code, connectOutcomeRejected},
		// Other token failures → terminal invalid token.
		{errors.New("jwtutil: signature invalid"), cfg.DisconnectInvalidToken.Code, connectOutcomeRejected},
		{errors.New("token expired"), cfg.DisconnectInvalidToken.Code, connectOutcomeRejected},
	}
	for _, tc := range cases {
		outcome, reply := connectVerifyFailure(tc.err)
		if code := replyCode(t, reply); code != tc.code || outcome != tc.outcome {
			t.Errorf("err=%v: code=%d outcome=%q, want %d %q", tc.err, code, outcome, tc.code, tc.outcome)
		}
	}
}

func replyCode(t *testing.T, reply error) uint32 {
	t.Helper()
	if d, ok := errors.AsType[cfg.Disconnect](reply); ok {
		return d.Code
	}
	if e, ok := errors.AsType[*cfg.Error](reply); ok {
		return e.Code
	}
	t.Fatalf("reply %T is neither a disconnect nor a client error", reply)
	return 0
}

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"empty", "", ""},
		{"token field", "abc.def", "abc.def"},
		{"bearer prefix", "Bearer abc.def", "abc.def"},
		{"bearer lower", "bearer abc.def", "abc.def"},
		{"whitespace", "  abc.def  ", "abc.def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractBearer(tc.token); got != tc.want {
				t.Fatalf("extractBearer = %q, want %q", got, tc.want)
			}
		})
	}
}
