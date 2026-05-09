package natsbackend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposeSubject_RoutingKeyOptional(t *testing.T) {
	// FR-074 [MED]: dots inside individual segments are URL-encoded
	// so "events" + "user.created" composes to a NATS subject with
	// exactly two dots (one boundary). Pre-fix the contract claimed
	// sanitisation but composeSubject just concatenated.
	assert.Equal(t, "events", composeSubject("events", ""))
	assert.Equal(t, "events.user%2Ecreated", composeSubject("events", "user.created"))
}

func TestSplitSubject_RoundTripsCompose(t *testing.T) {
	tests := []struct {
		subject  string
		exchange string
		routing  string
	}{
		{"events", "events", ""},
		{"events.user.created", "events", "user.created"},
		{"plain", "plain", ""},
	}
	for _, tt := range tests {
		ex, rk := splitSubject(tt.subject)
		assert.Equal(t, tt.exchange, ex, "subject=%q", tt.subject)
		assert.Equal(t, tt.routing, rk, "subject=%q", tt.subject)
	}
}

func TestConnect_RejectsEmptyURL(t *testing.T) {
	_, err := Connect(t.Context(), Config{})
	assert.Error(t, err)
}

// TestNewConsumer_DefaultsMaxDeliverTo5 pins the v1 H-3 audit fix:
// without a cap, JetStream's default of -1 (unlimited) means a
// poison-pill message that reliably triggers a panic in the handler
// gets nacked forever and blocks the consumer's progress. The fix
// sets MaxDeliver=5 when the operator hasn't supplied a value, so
// JetStream gives up after 5 attempts and either drops or routes to
// the configured DLQ.
func TestNewConsumer_DefaultsMaxDeliverTo5(t *testing.T) {
	c := NewConsumer(&Connection{}, ConsumerConfig{
		Stream:  "events",
		Durable: "consumer-1",
	}, nil)
	assert.Equal(t, 5, c.cfg.MaxDeliver,
		"NewConsumer must default MaxDeliver to 5 to cap poison-pill redelivery")
}

// TestNewConsumer_RespectsExplicitMaxDeliver confirms the operator
// can override the default — including with a negative value, which
// opts into JetStream's unlimited-redelivery semantics for callers
// that genuinely want it.
func TestNewConsumer_RespectsExplicitMaxDeliver(t *testing.T) {
	for _, n := range []int{1, 5, 100, -1} {
		c := NewConsumer(&Connection{}, ConsumerConfig{
			Stream:     "events",
			Durable:    "consumer-1",
			MaxDeliver: n,
		}, nil)
		assert.Equal(t, n, c.cfg.MaxDeliver, "MaxDeliver=%d must be honoured verbatim", n)
	}
}

// TestNewPublisher_DefaultPublishAckWait pins the v2 fix: when no option
// or threaded config is supplied, the publisher uses
// [defaultPublishAckWait].
func TestNewPublisher_DefaultPublishAckWait(t *testing.T) {
	p := NewPublisher(&Connection{})
	assert.Equal(t, defaultPublishAckWait, p.wait)
}

// TestNewPublisher_RespectsWithPublishAckWait pins that operators can
// override the default through the publisher option.
func TestNewPublisher_RespectsWithPublishAckWait(t *testing.T) {
	p := NewPublisher(&Connection{}, WithPublishAckWait(2*time.Second))
	assert.Equal(t, 2*time.Second, p.wait)
}

// TestConnection_NewPublisher_ThreadsPublishAckWait pins the v2 fix:
// Config.PublishAckWait must reach the publisher when constructed via
// Connection.NewPublisher (the codex finding was that this field was
// dead).
func TestConnection_NewPublisher_ThreadsPublishAckWait(t *testing.T) {
	conn := &Connection{publishAckWait: 250 * time.Millisecond}
	p := conn.NewPublisher()
	assert.Equal(t, 250*time.Millisecond, p.wait,
		"Config.PublishAckWait must thread through Connection.NewPublisher")
}

// TestConnection_NewPublisher_ZeroAckWaitFallsBackToDefault confirms
// that an unset PublishAckWait keeps the existing 5s default rather
// than disabling the timeout entirely.
func TestConnection_NewPublisher_ZeroAckWaitFallsBackToDefault(t *testing.T) {
	conn := &Connection{}
	p := conn.NewPublisher()
	assert.Equal(t, defaultPublishAckWait, p.wait)
}

// TestConnect_RespectsCancelledContext pins the v2 fix that Connect
// honours ctx cancellation. We give the dial a cancelled ctx and
// expect Connect to return promptly.
func TestConnect_RespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := Connect(ctx, Config{URL: "nats://127.0.0.1:4222", AllowInsecure: true})
	elapsed := time.Since(start)

	assert.Error(t, err, "cancelled ctx must abort Connect")
	assert.Less(t, elapsed, 2*time.Second,
		"cancelled ctx must abort the dial well below the 2s nats default")
}

// TestConnect_DerivesTimeoutFromDeadline ensures a near-deadline ctx
// short-circuits the dial rather than waiting for the nats default
// timeout.
func TestConnect_DerivesTimeoutFromDeadline(t *testing.T) {
	// Use an unroutable port so dial never succeeds. With a 100ms ctx
	// deadline the dial must give up within ~that window.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Connect(ctx, Config{URL: "nats://127.0.0.1:1", AllowInsecure: true})
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Less(t, elapsed, time.Second,
		"ctx deadline must drive nats.Timeout; got %s", elapsed)
}

// FR-073 [HIGH] regression: the kit must refuse to dial a plaintext
// NATS endpoint with no authentication. Pre-fix the Config exposed
// only URL/Name/ack/reconnect, so production deployments commonly
// shipped with no auth configured.
func TestConnect_RejectsPlaintextWithoutAuth(t *testing.T) {
	_, err := Connect(t.Context(), Config{URL: "nats://broker:4222"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FR-073")
}

// Companion: the explicit opt-out must work so legitimate single-host
// dev setups can still connect.
func TestConnect_AllowsInsecureWhenOptedIn(t *testing.T) {
	// We don't actually expect a successful dial — the broker isn't
	// running. The point is the validation MUST pass before the dial,
	// and the only way to observe that is to see a dial-side error
	// (not the FR-073 sentinel).
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, Config{URL: "nats://127.0.0.1:1", AllowInsecure: true})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "FR-073")
}

// TLS scheme satisfies the FR-073 check even without an explicit
// *tls.Config — nats.go falls back to the system trust store, but
// the connection is still encrypted.
func TestConnect_TLSSchemeSatisfiesAuthCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, Config{URL: "tls://127.0.0.1:1"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "FR-073")
}

// Username-based auth satisfies the FR-073 check.
func TestConnect_UsernamePasswordSatisfiesAuthCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, Config{URL: "nats://127.0.0.1:1", Username: "u", Password: "p"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "FR-073")
}

// fakeJetstreamMsg is a minimal jetstream.Msg implementation used by the
// extractor tests. We only need Subject() and Headers().
type fakeJetstreamMsg struct {
	jetstream.Msg
	subject string
	headers nats.Header
}

func (f *fakeJetstreamMsg) Subject() string                    { return f.subject }
func (f *fakeJetstreamMsg) Headers() nats.Header               { return f.headers }
func (f *fakeJetstreamMsg) Data() []byte                       { return nil }
func (f *fakeJetstreamMsg) Reply() string                      { return "" }
func (f *fakeJetstreamMsg) Ack() error                         { return nil }
func (f *fakeJetstreamMsg) DoubleAck(_ context.Context) error  { return nil }
func (f *fakeJetstreamMsg) Nak() error                         { return nil }
func (f *fakeJetstreamMsg) NakWithDelay(_ time.Duration) error { return nil }
func (f *fakeJetstreamMsg) InProgress() error                  { return nil }
func (f *fakeJetstreamMsg) Term() error                        { return nil }
func (f *fakeJetstreamMsg) TermWithReason(_ string) error      { return nil }

func (f *fakeJetstreamMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return nil, errors.New("fake jetstream msg: no metadata")
}

// TestExtractExchangeAndRoutingKey_PrefersHeaders pins the v2 fix for
// dotted exchange names: when X-Exchange and X-Routing-Key are present
// they win over a naive subject split. Without this, a publish with
// exchange="orders.v1" and routingKey="created" would be mis-routed as
// exchange="orders", routingKey="v1.created".
func TestExtractExchangeAndRoutingKey_PrefersHeaders(t *testing.T) {
	jm := &fakeJetstreamMsg{
		subject: "orders.v1.created",
		headers: nats.Header{
			headerExchange:   []string{"orders.v1"},
			headerRoutingKey: []string{"created"},
		},
	}
	ex, rk := extractExchangeAndRoutingKey(jm)
	assert.Equal(t, "orders.v1", ex)
	assert.Equal(t, "created", rk)
}

// TestExtractExchangeAndRoutingKey_FallsBackToSubject covers messages
// produced by older clients that did not set the X-Exchange /
// X-Routing-Key headers.
func TestExtractExchangeAndRoutingKey_FallsBackToSubject(t *testing.T) {
	jm := &fakeJetstreamMsg{
		subject: "events.user.created",
		headers: nats.Header{},
	}
	ex, rk := extractExchangeAndRoutingKey(jm)
	assert.Equal(t, "events", ex)
	assert.Equal(t, "user.created", rk)
}

// TestDrainWithTimeout_HangingDrainForceCloses pins the v2 fix that
// Close() respects [closeDrainTimeout]: a drain that never returns must
// not stall shutdown. The fake drain blocks forever; Close must invoke
// the fake close and surface a force-closed error inside the timeout.
func TestDrainWithTimeout_HangingDrainForceCloses(t *testing.T) {
	closed := make(chan struct{}, 1)
	drain := func() error { select {} }
	closeFn := func() { closed <- struct{}{} }

	start := time.Now()
	err := drainWithTimeout(drain, closeFn, 50*time.Millisecond)
	elapsed := time.Since(start)

	assert.Error(t, err, "force-close must surface as an error")
	assert.Contains(t, err.Error(), "force-closed")
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)
	assert.Less(t, elapsed, 500*time.Millisecond, "must not wait far past timeout")
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("force-close must invoke close() when drain hangs")
	}
}

// TestDrainWithTimeout_FastDrainReturnsCleanly verifies the happy path —
// a drain that completes inside the timeout surfaces its own return value
// and does NOT trigger force-close.
func TestDrainWithTimeout_FastDrainReturnsCleanly(t *testing.T) {
	drainErr := errors.New("drain done")
	drain := func() error { return drainErr }
	var closed bool
	closeFn := func() { closed = true }

	err := drainWithTimeout(drain, closeFn, time.Second)
	assert.ErrorIs(t, err, drainErr)
	assert.False(t, closed, "fast drain must not trigger force-close")
}

// TestExtractExchangeAndRoutingKey_HeaderOnlyExchange covers the
// routing-key-empty case where only X-Exchange is non-empty.
func TestExtractExchangeAndRoutingKey_HeaderOnlyExchange(t *testing.T) {
	jm := &fakeJetstreamMsg{
		subject: "orders.v1",
		headers: nats.Header{
			headerExchange:   []string{"orders.v1"},
			headerRoutingKey: []string{""},
		},
	}
	ex, rk := extractExchangeAndRoutingKey(jm)
	assert.Equal(t, "orders.v1", ex)
	assert.Equal(t, "", rk)
}
