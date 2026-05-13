package natsbackend

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

var _ slog.LogValuer = Config{}

func TestConfig_LogValue_RedactsURLCredentials(t *testing.T) {
	cfg := Config{URL: "nats://token-user:secret-pass@tenant-nats.internal:4222?token=query-secret#frag", Name: "orders-secret"}

	rendered := cfg.LogValue().String()

	assert.NotContains(t, rendered, "token-user")
	assert.NotContains(t, rendered, "secret-pass")
	assert.NotContains(t, rendered, "query-secret")
	assert.NotContains(t, rendered, "tenant-nats.internal")
	assert.NotContains(t, rendered, "orders-secret")
	assert.Contains(t, rendered, "url_configured=true")
	assert.Contains(t, rendered, "url_valid=true")
	assert.Contains(t, rendered, "host_configured=true")
	assert.Contains(t, rendered, "name_configured=true")
	assert.Contains(t, rendered, "username_configured=true")
	assert.Contains(t, rendered, "password_configured=true")
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"nats", "nats://nats.example.com:4222", false},
		{"tls", "tls://nats.example.com:4222", false},
		{"websocket", "ws://nats.example.com:4222", false},
		{"secure websocket", "wss://nats.example.com:4222", false},
		{"empty", "", true},
		{"missing host", "nats:///events", true},
		{"empty hostname", "nats://:4222/events", true},
		{"empty port", "nats://nats.example.com:/events", true},
		{"zero port", "nats://nats.example.com:0/events", true},
		{"too large port", "nats://nats.example.com:65536/events", true},
		{"zone identifier", "nats://[fe80::1%25lo0]:4222/events", true},
		{"unsupported scheme", "http://nats.example.com:4222", true},
		{"credentials", "nats://user:pass@nats.example.com:4222", true},
		{"query", "nats://nats.example.com:4222?token=abc", true},
		{"fragment", "nats://nats.example.com:4222#frag", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateURL_ParseErrorDoesNotEchoValue(t *testing.T) {
	err := ValidateURL("nats://nats.example.com/%zz?token=secret-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL is invalid")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestValidateURL_SchemeErrorDoesNotEchoValue(t *testing.T) {
	err := ValidateURL("secret-token://nats.example.com:4222")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL scheme must be nats")
	assert.NotContains(t, err.Error(), "secret-token")
}

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

func TestConnect_RejectsNilContext(t *testing.T) {
	var ctx context.Context
	_, err := Connect(ctx, Config{URL: "nats://127.0.0.1:4222", AllowInsecure: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestConnect_RejectsInvalidURLBeforeDial(t *testing.T) {
	tests := []string{
		"http://nats.example.com:4222",
		"nats:///events",
		"nats://user:pass@nats.example.com:4222",
		"nats://nats.example.com:4222?token=abc",
	}
	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			_, err := Connect(t.Context(), Config{URL: rawURL, AllowInsecure: true})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "connect:")
		})
	}
}

func TestConnect_RejectsNegativeTimingConfig(t *testing.T) {
	base := Config{URL: "nats://example.invalid:4222"}
	for name, mutate := range map[string]func(*Config){
		"PublishAckWait": func(c *Config) { c.PublishAckWait = -time.Second },
		"MaxReconnects":  func(c *Config) { c.MaxReconnects = -2 },
		"ReconnectWait":  func(c *Config) { c.ReconnectWait = -time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			_, err := Connect(t.Context(), cfg)
			require.Error(t, err)
		})
	}
}

func TestValidateAuth_AcceptsRotatingCredentialProviders(t *testing.T) {
	serverURL := &url.URL{Scheme: "nats", Host: "nats.example.com:4222"}

	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "username password provider",
			cfg: Config{
				UsernamePasswordProvider: func() (string, string) {
					return "user", "rotated-password"
				},
			},
		},
		{
			name: "token provider",
			cfg: Config{
				TokenProvider: func() string {
					return "rotated-token"
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.cfg.validateAuth(serverURL))
		})
	}
}

func TestCloneTLSConfigWithFloor_ClonesAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{ServerName: "nats.internal.test"}
	cfg.MinVersion = minimumTLSVersion - 1

	cloned, err := cloneTLSConfigWithFloor(cfg)
	require.NoError(t, err)
	require.NotNil(t, cloned)
	assert.NotSame(t, cfg, cloned)
	assert.Equal(t, uint16(minimumTLSVersion-1), cfg.MinVersion)
	assert.Equal(t, uint16(minimumTLSVersion), cloned.MinVersion)
	assert.Equal(t, "nats.internal.test", cloned.ServerName)
}

func TestCloneTLSConfigWithFloor_RejectsMaxVersionBelowFloor(t *testing.T) {
	_, err := cloneTLSConfigWithFloor(&tls.Config{MaxVersion: minimumTLSVersion - 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS MaxVersion")
}

func TestConfigClone_ClonesTLSAndExtraOptions(t *testing.T) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS10,
		NextProtos: []string{"h2"},
		ServerName: "before.example",
	}
	extraOptions := []nats.Option{nats.NoReconnect()}
	cfg := Config{
		URL:          "nats://localhost:4222",
		TLS:          tlsCfg,
		ExtraOptions: extraOptions,
	}

	cloned, err := cfg.Clone()
	require.NoError(t, err)
	require.NotNil(t, cloned.TLS)
	require.Len(t, cloned.ExtraOptions, 1)
	assert.NotSame(t, tlsCfg, cloned.TLS)
	assert.Equal(t, uint16(minimumTLSVersion), cloned.TLS.MinVersion)
	assert.Equal(t, "before.example", cloned.TLS.ServerName)
	assert.NotNil(t, cloned.ExtraOptions[0])

	tlsCfg.ServerName = "after.example"
	tlsCfg.NextProtos[0] = "http/1.1"
	extraOptions[0] = nil

	assert.Equal(t, "before.example", cloned.TLS.ServerName)
	assert.Equal(t, []string{"h2"}, cloned.TLS.NextProtos)
	assert.NotNil(t, cloned.ExtraOptions[0])
}

func TestConfigClone_RejectsTLSMaxVersionBelowFloor(t *testing.T) {
	_, err := (Config{TLS: &tls.Config{MaxVersion: minimumTLSVersion - 1}}).Clone()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS MaxVersion")
}

func TestConnect_RejectsTLSMaxVersionBelowFloor(t *testing.T) {
	_, err := Connect(t.Context(), Config{
		URL: "nats://127.0.0.1:4222",
		TLS: &tls.Config{MaxVersion: minimumTLSVersion - 1},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS MaxVersion")
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

func TestNewConsumer_PanicsOnNegativeTimingConfig(t *testing.T) {
	for name, cfg := range map[string]ConsumerConfig{
		"MaxAckPending": {Stream: "events", Durable: "consumer-1", MaxAckPending: -1},
		"AckWait":       {Stream: "events", Durable: "consumer-1", AckWait: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, func() {
				NewConsumer(&Connection{}, cfg, nil)
			})
		})
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

func TestNewPublisher_MessageSizeOptions(t *testing.T) {
	p := NewPublisher(&Connection{},
		WithMaxMessageBytes(64),
		WithRouteMaxMessageBytes("events", "large.event", 512),
	)

	assert.Equal(t, 64, p.sizeLimiter.LimitFor("events", "small.event"))
	assert.Equal(t, 512, p.sizeLimiter.LimitFor("events", "large.event"))
}

func TestNewPublisher_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewPublisher(&Connection{}, nil)
	})
}

func TestWithPublishAckWait_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			require.Panics(t, func() {
				WithPublishAckWait(d)
			})
		})
	}
}

func TestWithMaxMessageBytes_PanicsOnNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			require.Panics(t, func() {
				NewPublisher(&Connection{}, WithMaxMessageBytes(n))
			})
		})
	}
}

func TestWithoutPublishAckWait_DisablesPublisherTimeout(t *testing.T) {
	p := NewPublisher(&Connection{}, WithoutPublishAckWait())
	assert.Zero(t, p.wait)
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

func TestConnection_InvalidReceiverSafety(t *testing.T) {
	var nilConn *Connection
	assert.False(t, nilConn.Healthy())
	assert.NoError(t, nilConn.Stop(context.Background()))
	assert.Nil(t, nilConn.JetStream())
	assert.Error(t, nilConn.EnsureStream(t.Context(), StreamConfig{Name: "events", Subjects: []string{"events.>"}}))

	zero := &Connection{}
	assert.False(t, zero.Healthy())
	assert.NoError(t, zero.Stop(context.Background()))
	assert.Nil(t, zero.JetStream())
	assert.Error(t, zero.EnsureStream(t.Context(), StreamConfig{Name: "events", Subjects: []string{"events.>"}}))
}

func TestPublisher_InvalidReceiverReturnsError(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", map[string]string{"ok": "true"})
	require.NoError(t, err)

	var nilPublisher *Publisher
	assert.ErrorIs(t, nilPublisher.Publish(t.Context(), "events", "created", msg), messaging.ErrInvalidPublisher)
	assert.ErrorIs(t, (&Publisher{}).Publish(t.Context(), "events", "created", msg), messaging.ErrInvalidPublisher)
	assert.ErrorIs(t, NewPublisher(&Connection{}).Publish(t.Context(), "events", "created", msg), messaging.ErrInvalidPublisher)
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
	data    []byte
	acked   bool
	nacked  bool
	termed  bool
}

func (f *fakeJetstreamMsg) Subject() string                    { return f.subject }
func (f *fakeJetstreamMsg) Headers() nats.Header               { return f.headers }
func (f *fakeJetstreamMsg) Data() []byte                       { return f.data }
func (f *fakeJetstreamMsg) Reply() string                      { return "" }
func (f *fakeJetstreamMsg) Ack() error                         { f.acked = true; return nil }
func (f *fakeJetstreamMsg) DoubleAck(_ context.Context) error  { return nil }
func (f *fakeJetstreamMsg) Nak() error                         { f.nacked = true; return nil }
func (f *fakeJetstreamMsg) NakWithDelay(_ time.Duration) error { return nil }
func (f *fakeJetstreamMsg) InProgress() error                  { return nil }
func (f *fakeJetstreamMsg) Term() error                        { f.termed = true; return nil }
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

func TestConsumer_InvalidReceiverReturnsError(t *testing.T) {
	var nilConsumer *Consumer
	err := nilConsumer.Consume(t.Context(), func(context.Context, messaging.Delivery) error { return nil })
	assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)

	err = (&Consumer{}).Consume(t.Context(), func(context.Context, messaging.Delivery) error { return nil })
	assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)

	err = NewConsumer(&Connection{}, ConsumerConfig{Stream: "events", Durable: "durable"}, slog.Default()).
		Consume(t.Context(), func(context.Context, messaging.Delivery) error { return nil })
	assert.ErrorIs(t, err, messaging.ErrInvalidConsumer)
}

func TestDispatch_PopulatesMessageHeadersAndDetachesMessage(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	jm := &fakeJetstreamMsg{
		subject: "orders.created",
		data:    data,
		headers: nats.Header{
			headerExchange:                []string{"orders"},
			headerRoutingKey:              []string{"created"},
			messaging.HeaderCorrelationID: []string{"corr-1"},
		},
	}
	c := &Consumer{logger: slog.Default()}

	var got messaging.Delivery
	c.dispatch(t.Context(), jm, func(_ context.Context, d messaging.Delivery) error {
		assert.Equal(t, "corr-1", d.Message.Headers[messaging.HeaderCorrelationID])
		got = d
		d.Message.Payload[1] = 'X'
		d.Message.Headers[messaging.HeaderCorrelationID] = "changed"
		return nil
	})

	require.True(t, jm.acked)
	assert.Equal(t, "orders", got.Exchange)
	assert.Equal(t, "created", got.RoutingKey)
	assert.Equal(t, "corr-1", got.Headers[messaging.HeaderCorrelationID])
	assert.Equal(t, `{"id":"42"}`, string(msg.Payload))
	assert.Equal(t, `{Xid":"42"}`, string(got.Message.Payload))
	assert.Equal(t, "corr-1", jm.headers.Get(messaging.HeaderCorrelationID))
}
