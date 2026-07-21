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

func TestConnectVerifyFailure_DistinguishesKeySetUnavailable(t *testing.T) {
	// Infrastructure: JWKS not ready / stale → temporary DisconnectServerError.
	for _, err := range []error{
		jwtutil.ErrKeySetUnavailable,
		jwtutil.ErrKeySetNotReady,
		jwtutil.ErrKeySetStale,
		fmt.Errorf("wrap: %w", jwtutil.ErrKeySetNotReady),
	} {
		disc, outcome := connectVerifyFailure(err)
		if disc.Code != cfg.DisconnectServerError.Code {
			t.Fatalf("err=%v: disconnect code=%d, want %d (server error)", err, disc.Code, cfg.DisconnectServerError.Code)
		}
		if outcome != connectOutcomeError {
			t.Fatalf("err=%v: outcome=%q, want %q", err, outcome, connectOutcomeError)
		}
	}

	// Genuine token failures → terminal invalid token.
	for _, err := range []error{
		errors.New("jwtutil: signature invalid"),
		fmt.Errorf("token expired"),
	} {
		disc, outcome := connectVerifyFailure(err)
		if disc.Code != cfg.DisconnectInvalidToken.Code {
			t.Fatalf("err=%v: disconnect code=%d, want %d (invalid token)", err, disc.Code, cfg.DisconnectInvalidToken.Code)
		}
		if outcome != connectOutcomeRejected {
			t.Fatalf("err=%v: outcome=%q, want %q", err, outcome, connectOutcomeRejected)
		}
	}
}

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		name  string
		token string
		data  []byte
		want  string
	}{
		{"empty", "", nil, ""},
		{"token field", "abc.def", nil, "abc.def"},
		{"bearer prefix", "Bearer abc.def", nil, "abc.def"},
		{"data field", "", []byte("xyz.tok"), "xyz.tok"},
		{"data bearer", "", []byte("Bearer xyz.tok"), "xyz.tok"},
		{"token wins over data", "from-token", []byte("from-data"), "from-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractBearer(tc.token, tc.data); got != tc.want {
				t.Fatalf("extractBearer = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConnectVerifyFailure_KeySetUnavailable(t *testing.T) {
	disc, outcome := connectVerifyFailure(fmt.Errorf("wrap: %w", jwtutil.ErrKeySetUnavailable))
	if disc.Code != cfg.DisconnectServerError.Code {
		t.Fatalf("disconnect code = %d, want server error", disc.Code)
	}
	if outcome != connectOutcomeError {
		t.Fatalf("outcome = %q, want %q", outcome, connectOutcomeError)
	}
}

func TestConnectVerifyFailure_InvalidToken(t *testing.T) {
	disc, outcome := connectVerifyFailure(errors.New("bad token"))
	if disc.Code != cfg.DisconnectInvalidToken.Code {
		t.Fatalf("disconnect code = %d, want invalid token", disc.Code)
	}
	if outcome != connectOutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
}
