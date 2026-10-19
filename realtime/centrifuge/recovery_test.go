package centrifuge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	cfg "github.com/centrifugal/centrifuge"
	"github.com/coder/websocket"
)

type subscribeResult struct {
	ID    int `json:"id"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
	Subscribe *struct {
		Recoverable bool   `json:"recoverable"`
		Positioned  bool   `json:"positioned"`
		Offset      uint64 `json:"offset"`
		Epoch       string `json:"epoch"`
	} `json:"subscribe"`
}

func readSubscribeResult(t *testing.T, ctx context.Context, conn *websocket.Conn, id int) subscribeResult {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var reply subscribeResult
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

func TestWithSubscriptionRecovery_AppliesAfterAuthorization(t *testing.T) {
	provider, sign := fixtureTokenProvider(t)
	var policyCalls atomic.Int64
	n, err := NewNode(WithJWTAuth(provider),
		WithSubscribeAuthorizer(func(_ context.Context, e ChannelAuthEvent) error {
			if e.Channel == "denied" {
				return cfg.ErrorPermissionDenied
			}
			return nil
		}),
		WithSubscriptionRecovery(func(channel string) SubscriptionRecovery {
			policyCalls.Add(1)
			switch channel {
			case "stream":
				return SubscriptionRecovery{Recovery: true}
			case "positioned":
				return SubscriptionRecovery{Positioning: true}
			}
			return SubscriptionRecovery{}
		}))
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, n)
	if _, err := n.Publish("stream", []byte(`{"v":1}`), cfg.WithHistory(10, time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Publish("positioned", []byte(`{"v":1}`), cfg.WithHistory(10, time.Minute)); err != nil {
		t.Fatal(err)
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{"token": sign(fixtureSubject)}})
	if r := readSocketReply(t, ctx, conn, 1); r.Error != nil {
		t.Fatal(r.Error)
	}

	socketCommand(t, ctx, conn, map[string]any{"id": 2, "subscribe": map[string]any{"channel": "denied"}})
	if r := readSubscribeResult(t, ctx, conn, 2); r.Error == nil {
		t.Fatal("denied channel must stay denied")
	}
	if policyCalls.Load() != 0 {
		t.Fatal("recovery policy must not run for a denied subscription")
	}

	socketCommand(t, ctx, conn, map[string]any{"id": 3, "subscribe": map[string]any{"channel": "stream"}})
	r := readSubscribeResult(t, ctx, conn, 3)
	if r.Error != nil || r.Subscribe == nil || !r.Subscribe.Recoverable || !r.Subscribe.Positioned || r.Subscribe.Offset != 1 || r.Subscribe.Epoch == "" {
		t.Fatalf("stream subscribe reply = %+v, want recoverable and positioned at offset 1", r.Subscribe)
	}

	socketCommand(t, ctx, conn, map[string]any{"id": 4, "subscribe": map[string]any{"channel": "positioned"}})
	r = readSubscribeResult(t, ctx, conn, 4)
	if r.Error != nil || r.Subscribe == nil || r.Subscribe.Recoverable || !r.Subscribe.Positioned {
		t.Fatalf("positioned subscribe reply = %+v, want positioned only", r.Subscribe)
	}

	socketCommand(t, ctx, conn, map[string]any{"id": 5, "subscribe": map[string]any{"channel": "plain"}})
	r = readSubscribeResult(t, ctx, conn, 5)
	if r.Error != nil || r.Subscribe == nil || r.Subscribe.Recoverable || r.Subscribe.Positioned {
		t.Fatalf("plain subscribe reply = %+v, want neither recovery nor positioning", r.Subscribe)
	}
	if policyCalls.Load() != 3 {
		t.Fatalf("policy calls = %d, want 3", policyCalls.Load())
	}
}

func TestSubscribe_DefaultHasNoRecovery(t *testing.T) {
	n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithOpenChannelsUnsafe())
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, n)
	if _, err := n.Publish("stream", []byte(`{}`), cfg.WithHistory(10, time.Minute)); err != nil {
		t.Fatal(err)
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{}})
	if r := readSocketReply(t, ctx, conn, 1); r.Error != nil {
		t.Fatal(r.Error)
	}
	socketCommand(t, ctx, conn, map[string]any{"id": 2, "subscribe": map[string]any{"channel": "stream"}})
	r := readSubscribeResult(t, ctx, conn, 2)
	if r.Error != nil || r.Subscribe == nil || r.Subscribe.Recoverable || r.Subscribe.Positioned {
		t.Fatalf("default subscribe reply = %+v, want no recovery", r.Subscribe)
	}
}
