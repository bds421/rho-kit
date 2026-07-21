package redisstream

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func testConsumerMetrics(t *testing.T) *ConsumerMetrics {
	t.Helper()
	return NewConsumerMetrics(WithConsumerMetricsRegisterer(prometheus.NewRegistry()))
}

// TestDeadLetter_DoesNotACKWhenXAddFails is the regression pin for
// review-15: if the dead-letter XADD fails, the source message must
// remain in the PEL (XACK must not run). Sequential XADD-then-XACK
// guarantees this; a pipeline would still execute XACK after XADD
// failure.
//
// miniredis does not easily inject per-command DENYOOM, so we pin the
// control-flow contract by asserting the call order through a
// scripted client that only implements XAdd/XAck used by deadLetter.
func TestDeadLetter_DoesNotACKWhenXAddFails(t *testing.T) {
	script := &scriptedDLClient{xaddFail: true}
	c := &Consumer{
		client:  script,
		group:   "g1",
		logger:  slog.Default(),
		metrics: testConsumerMetrics(t),
	}
	c.deadLetter(context.Background(), "src-stream", "dlq-stream", goredis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"k": "v"},
	}, "boom")
	require.Equal(t, []string{"XADD"}, script.order,
		"XACK must not run when XADD fails — message stays pending")
	require.False(t, script.acked, "source must not be ACKed after failed dead-letter")
}

// TestDeadLetter_ACKAfterSuccessfulXAdd pins the success path order.
func TestDeadLetter_ACKAfterSuccessfulXAdd(t *testing.T) {
	script := &scriptedDLClient{}
	c := &Consumer{
		client:  script,
		group:   "g1",
		logger:  slog.Default(),
		metrics: testConsumerMetrics(t),
	}
	c.deadLetter(context.Background(), "src-stream", "dlq-stream", goredis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"k": "v"},
	}, "boom")
	require.Equal(t, []string{"XADD", "XACK"}, script.order)
	require.True(t, script.acked)
}

// scriptedDLClient implements only the UniversalClient methods
// deadLetter invokes. Other methods panic if touched.
type scriptedDLClient struct {
	goredis.UniversalClient // nil embed for unused methods (never called)
	order                   []string
	xaddFail                bool
	acked                   bool
}

func (s *scriptedDLClient) XAdd(ctx context.Context, a *goredis.XAddArgs) *goredis.StringCmd {
	s.order = append(s.order, "XADD")
	cmd := goredis.NewStringCmd(ctx, "xadd")
	if s.xaddFail {
		cmd.SetErr(context.DeadlineExceeded)
		return cmd
	}
	cmd.SetVal("1-0")
	return cmd
}

func (s *scriptedDLClient) XAck(ctx context.Context, stream, group string, ids ...string) *goredis.IntCmd {
	s.order = append(s.order, "XACK")
	s.acked = true
	cmd := goredis.NewIntCmd(ctx, "xack")
	cmd.SetVal(1)
	return cmd
}
