//go:build integration

package centrifuge

import (
	"os"
	"testing"
	"time"

	cfg "github.com/centrifugal/centrifuge"
)

// TestRedisBackend_SharesHistoryAcrossNodes runs two nodes against one Redis
// and proves a publication made on one is visible in the other's history.
// It needs a reachable Redis: set CENTRIFUGE_TEST_REDIS_ADDR (for example
// 127.0.0.1:6379). The base module does not import the Docker-backed redistest
// helper, so the test skips when the variable is unset.
func TestRedisBackend_SharesHistoryAcrossNodes(t *testing.T) {
	addr := os.Getenv("CENTRIFUGE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CENTRIFUGE_TEST_REDIS_ADDR not set")
	}
	backend := RedisBackendConfig{
		Shards:          []cfg.RedisShardConfig{{Address: addr}},
		Prefix:          "kit-it-" + time.Now().Format("150405.000000000"),
		PlaintextUnsafe: true,
	}
	newNode := func() *Node {
		n, err := NewNode(WithAnonymousConnectionsUnsafe(), WithBackend(RedisBackend(backend)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, b := newNode(), newNode()
	stopA := startNode(t, a)
	defer stopA()
	stopB := startNode(t, b)
	defer stopB()

	if _, err := a.Publish("updates", []byte(`{"v":1}`), cfg.WithHistory(10, time.Minute)); err != nil {
		t.Fatal(err)
	}
	history, err := b.Underlying().History("updates", cfg.WithLimit(cfg.NoLimit))
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Publications) != 1 || history.Offset != 1 {
		t.Fatalf("history on second node = %+v, want one publication at offset 1", history)
	}
}
