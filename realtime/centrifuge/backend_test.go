package centrifuge

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cfg "github.com/centrifugal/centrifuge"
)

// countingBroker wraps the native memory broker so a test can prove the
// configured backend, not the default, served the node.
type countingBroker struct {
	*cfg.MemoryBroker
	publishes atomic.Int64
}

func (b *countingBroker) Publish(ch string, data []byte, opts cfg.PublishOptions) (cfg.PublishResult, error) {
	b.publishes.Add(1)
	return b.MemoryBroker.Publish(ch, data, opts)
}

func startNode(t *testing.T, n *Node) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()
	deadline := time.After(5 * time.Second)
	for !n.Ready() {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Start returned early: %v", err)
		case <-deadline:
			cancel()
			t.Fatal("node never ready")
		case <-time.After(time.Millisecond):
		}
	}
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if err := n.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}
}

func TestWithBackend_InstalledOnStartAndClosedOnStop(t *testing.T) {
	var broker *countingBroker
	var built, closed atomic.Int64
	n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithBackend(func(node *cfg.Node) (Backend, error) {
		built.Add(1)
		mem, err := cfg.NewMemoryBroker(node, cfg.MemoryBrokerConfig{})
		if err != nil {
			return Backend{}, err
		}
		broker = &countingBroker{MemoryBroker: mem}
		return Backend{Broker: broker, Close: func() { closed.Add(1) }}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if built.Load() != 0 {
		t.Fatal("backend must not be built at construction")
	}
	stop := startNode(t, n)
	if _, err := n.Publish("updates", []byte(`{}`), cfg.WithHistory(10, time.Minute)); err != nil {
		t.Fatal(err)
	}
	if broker.publishes.Load() != 1 {
		t.Fatalf("configured broker publishes = %d, want 1", broker.publishes.Load())
	}
	history, err := n.Underlying().History("updates", cfg.WithLimit(cfg.NoLimit))
	if err != nil || len(history.Publications) != 1 {
		t.Fatalf("history = %+v, %v; want one publication", history, err)
	}
	stop()
	if built.Load() != 1 || closed.Load() != 1 {
		t.Fatalf("built=%d closed=%d, want 1/1", built.Load(), closed.Load())
	}
	if err := n.Stop(context.Background()); err != nil || closed.Load() != 1 {
		t.Fatalf("second Stop: err=%v closed=%d", err, closed.Load())
	}
}

func TestWithBackend_FactoryErrorsFailStartAndAllowRetry(t *testing.T) {
	var calls atomic.Int64
	n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithBackend(func(node *cfg.Node) (Backend, error) {
		if calls.Add(1) == 1 {
			return Backend{}, errors.New("secret dial detail")
		}
		mem, err := cfg.NewMemoryBroker(node, cfg.MemoryBrokerConfig{})
		return Backend{Broker: mem}, err
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = n.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "backend") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Start error = %v, want redacted backend error", err)
	}
	if n.Ready() {
		t.Fatal("failed Start must not be ready")
	}
	stop := startNode(t, n)
	stop()
}

func TestWithBackend_RejectsEmptyBackend(t *testing.T) {
	var closed atomic.Bool
	n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithBackend(func(*cfg.Node) (Backend, error) {
		return Backend{Close: func() { closed.Store(true) }}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "neither broker nor presence manager") {
		t.Fatalf("Start error = %v", err)
	}
	if !closed.Load() {
		t.Fatal("Close must run for a rejected backend")
	}
}

func TestBackendOptionPanics(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithBackend(nil)":        func() { WithBackend(nil) },
		"RedisBackend(no shards)": func() { RedisBackend(RedisBackendConfig{}) },
		"RedisBackend(negative TTL)": func() {
			RedisBackend(RedisBackendConfig{Shards: []cfg.RedisShardConfig{{Address: "127.0.0.1:6379"}}, PresenceTTL: -1})
		},
		"RedisBackend(plaintext)": func() {
			RedisBackend(RedisBackendConfig{Shards: []cfg.RedisShardConfig{{Address: "redis.internal:6379", Password: "pw"}}})
		},
		"WithSubscriptionRecovery(nil)": func() { WithSubscriptionRecovery(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected panic from %s", name)
				}
			}()
			fn()
		})
	}
}

func TestCheckRedisShardTransport(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	cases := []struct {
		name  string
		shard cfg.RedisShardConfig
		ok    bool
	}{
		{"loopback plaintext", cfg.RedisShardConfig{Address: "127.0.0.1:6379"}, true},
		{"localhost url", cfg.RedisShardConfig{Address: "redis://localhost:6379/0"}, true},
		{"unix socket", cfg.RedisShardConfig{Address: "unix:///var/run/redis.sock"}, true},
		{"remote tls password", cfg.RedisShardConfig{Address: "redis.internal:6379", TLSConfig: tlsCfg, Password: "pw"}, true},
		{"remote rediss url password", cfg.RedisShardConfig{Address: "rediss://:pw@redis.internal:6379"}, true},
		{"remote plaintext", cfg.RedisShardConfig{Address: "redis.internal:6379", Password: "pw"}, false},
		{"remote redis url", cfg.RedisShardConfig{Address: "redis://:pw@redis.internal:6379"}, false},
		{"remote tls no password", cfg.RedisShardConfig{Address: "redis.internal:6379", TLSConfig: tlsCfg}, false},
		{"cluster mixed plaintext", cfg.RedisShardConfig{ClusterAddresses: []string{"127.0.0.1:7000", "10.0.0.2:7000"}, Password: "pw"}, false},
		{"cluster loopback", cfg.RedisShardConfig{ClusterAddresses: []string{"127.0.0.1:7000", "[::1]:7001"}}, true},
		{"sentinel without sentinel tls", cfg.RedisShardConfig{SentinelAddresses: []string{"10.0.0.3:26379"}, TLSConfig: tlsCfg, Password: "pw"}, false},
		{"sentinel with tls", cfg.RedisShardConfig{SentinelAddresses: []string{"10.0.0.3:26379"}, SentinelTLSConfig: tlsCfg, TLSConfig: tlsCfg, Password: "pw"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRedisShardTransport(tc.shard)
			if (err == nil) != tc.ok {
				t.Fatalf("checkRedisShardTransport() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// TestRedisBackend_UnreachableShardFailsStart proves shard dialing happens in
// Start (not NewNode) and that a dial failure fails Start without leaking a
// ready node. No Redis is required: the loopback port refuses connections.
func TestRedisBackend_UnreachableShardFailsStart(t *testing.T) {
	n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithBackend(RedisBackend(RedisBackendConfig{
		Shards: []cfg.RedisShardConfig{{Address: "127.0.0.1:1", ConnectTimeout: 200 * time.Millisecond}},
	})))
	if err != nil {
		t.Fatalf("NewNode must not dial Redis: %v", err)
	}
	if err := n.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("Start error = %v, want backend error", err)
	}
	if n.Ready() {
		t.Fatal("node must not be ready")
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPublish_RejectsEmptyChannel(t *testing.T) {
	n, err := NewNode(WithAnonymousConnectionsUnsafe())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Publish("", []byte(`{}`)); err == nil {
		t.Fatal("Publish with empty channel must fail")
	}
}
