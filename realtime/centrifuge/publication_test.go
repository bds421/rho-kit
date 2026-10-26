package centrifuge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bds421/rho-kit/security/v2/jwtutil"

	cfg "github.com/centrifugal/centrifuge"
	"github.com/centrifugal/protocol"
)

func TestReadyTracksActualStartContext(t *testing.T) {
	n, err := NewNode(WithAnonymousConnectionsUnsafe())
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready() || (*Node)(nil).Ready() {
		t.Fatal("unstarted node ready")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Start(ctx) }()
	deadline := time.After(5 * time.Second)
	for !n.Ready() {
		select {
		case <-deadline:
			t.Fatal("native Run never ready")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n.Ready() {
		t.Fatal("canceled Start remains ready")
	}
	if err := n.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionReadyRequiresSuccessfulInstalledChannel(t *testing.T) {
	provider, sign := fixtureTokenProvider(t)
	observed := make(chan string, 4)
	var authorized atomic.Int64
	n, err := NewNode(WithJWTAuth(provider), WithClientChannelLimit(1),
		WithSubscribeAuthorizer(func(_ context.Context, e ChannelAuthEvent) error {
			authorized.Add(1)
			if e.Channel == "denied" {
				return cfg.ErrorPermissionDenied
			}
			return nil
		}), WithSubscriptionReadyObserver(func(c Connection, ch string) {
			if !slices.Contains(c.client.Channels(), ch) {
				t.Error("observer before installation")
			}
			observed <- ch
		}))
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, n)
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{"token": sign(fixtureSubject)}})
	if r := readSocketReply(t, ctx, conn, 1); r.Error != nil {
		t.Fatal(r.Error)
	}
	for i, ch := range []string{"denied", "allowed", "allowed", "over-limit"} {
		socketCommand(t, ctx, conn, map[string]any{"id": i + 2, "subscribe": map[string]any{"channel": ch}})
		r := readSocketReply(t, ctx, conn, i+2)
		if (i == 1) != (r.Error == nil) {
			t.Fatalf("unexpected subscribe result for %s: %+v", ch, r)
		}
	}
	select {
	case ch := <-observed:
		if ch != "allowed" {
			t.Fatal(ch)
		}
	case <-ctx.Done():
		t.Fatal("ready observer missing")
	}
	// A round trip has completed every callback above. No denied/duplicate/limit
	// failure may produce another ready notification.
	select {
	case ch := <-observed:
		t.Fatalf("failed subscription observed: %s", ch)
	default:
	}
	if authorized.Load() != 2 {
		t.Fatalf("authorization chain changed: %d", authorized.Load())
	}
}

func TestSubscriptionReadyDoesNotObserveCanceledConnection(t *testing.T) {
	provider, sign := fixtureTokenProvider(t)
	var cancel context.CancelFunc
	var observations atomic.Int64
	n, err := NewNode(WithJWTAuth(provider),
		WithVerifiedConnectPolicy(func(ctx context.Context, _ cfg.ConnectEvent, _ jwtutil.Claims) (ConnectionPolicyReply, error) {
			child, stop := context.WithCancel(ctx)
			cancel = stop
			return ConnectionPolicyReply{Context: child}, nil
		}), WithSubscribeAuthorizer(func(context.Context, ChannelAuthEvent) error { cancel(); return nil }),
		WithSubscriptionReadyObserver(func(Connection, string) { observations.Add(1) }))
	if err != nil {
		t.Fatal(err)
	}
	conn, ctx := fixtureSocket(t, n)
	socketCommand(t, ctx, conn, map[string]any{"id": 1, "connect": map[string]any{"token": sign(fixtureSubject)}})
	if r := readSocketReply(t, ctx, conn, 1); r.Error != nil {
		t.Fatal(r.Error)
	}
	t.Cleanup(cancel)
	socketCommand(t, ctx, conn, map[string]any{"id": 2, "subscribe": map[string]any{"channel": "fixture"}})
	readSocketReply(t, ctx, conn, 2)
	if observations.Load() != 0 {
		t.Fatal("canceled connection observed ready")
	}
}

// A test transport blocks a control write so native publications accumulate in
// the real client's queue. It never implements a production protocol writer.
type queuedTransport struct {
	mu             sync.Mutex
	frames         [][]byte
	batchSizes     []int
	wire           publicationWire
	blocked        chan struct{}
	closed         chan struct{}
	blockError     error
	block, release func()
	close          func()
}

func (*queuedTransport) Name() string                         { return "fixture" }
func (*queuedTransport) AcceptProtocol() string               { return "" }
func (q *queuedTransport) Protocol() cfg.ProtocolType         { return q.wire.protocol }
func (*queuedTransport) ProtocolVersion() cfg.ProtocolVersion { return cfg.ProtocolVersion2 }
func (q *queuedTransport) Unidirectional() bool               { return q.wire.unidirectional }
func (*queuedTransport) Emulation() bool                      { return false }
func (*queuedTransport) DisabledPushFlags() uint64            { return 0 }
func (*queuedTransport) PingPongConfig() cfg.PingPongConfig   { return cfg.PingPongConfig{} }
func (q *queuedTransport) Write(b []byte) error {
	if bytes.Contains(b, []byte("block-control")) {
		q.block()
		if q.blockError != nil {
			return q.blockError
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.frames = append(q.frames, bytes.Clone(b))
	return nil
}
func (q *queuedTransport) WriteMany(bs ...[]byte) error {
	q.mu.Lock()
	q.batchSizes = append(q.batchSizes, len(bs))
	q.mu.Unlock()
	for _, b := range bs {
		if err := q.Write(b); err != nil {
			return err
		}
	}
	return nil
}
func (q *queuedTransport) Close(cfg.Disconnect) error {
	q.close()
	return nil
}

type publicationWire struct {
	name           string
	protocol       cfg.ProtocolType
	unidirectional bool
}

func publicationWires() []publicationWire {
	return []publicationWire{
		{"json-bidi", cfg.ProtocolTypeJSON, false},
		{"json-uni", cfg.ProtocolTypeJSON, true},
		{"protobuf-bidi", cfg.ProtocolTypeProtobuf, false},
		{"protobuf-uni", cfg.ProtocolTypeProtobuf, true},
	}
}

func nativePublicationClient(t *testing.T, ctx context.Context, wire publicationWire, policy func(Connection, PublicationWriteEvent) bool) (*Node, *cfg.Client, *queuedTransport) {
	t.Helper()
	provider, sign := fixtureTokenProvider(t)
	n, err := NewNode(WithJWTAuth(provider), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithClientQueueMaxSize(1<<20), WithPublicationWritePolicy(policy))
	if err != nil {
		t.Fatal(err)
	}
	run, cancelRun := context.WithCancel(t.Context())
	started := make(chan error, 1)
	go func() { started <- n.Start(run) }()
	t.Cleanup(func() {
		cancelRun()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := n.Stop(cleanup); err != nil {
			t.Error(err)
		}
		select {
		case err := <-started:
			if err != nil {
				t.Error(err)
			}
		case <-cleanup.Done():
			t.Error("native Start did not join")
		}
	})
	waitPublicationCondition(t, n.Ready, "native Start never ready")
	release := make(chan struct{})
	q := &queuedTransport{wire: wire, blocked: make(chan struct{}), closed: make(chan struct{})}
	q.block = sync.OnceFunc(func() { close(q.blocked); <-release })
	q.release = sync.OnceFunc(func() { close(release) })
	q.close = sync.OnceFunc(func() { close(q.closed) })
	c, closeClient, err := cfg.NewClient(ctx, n.Underlying(), q)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.release(); _ = closeClient() })
	if wire.unidirectional {
		if err := c.ConnectNoErrorToDisconnect(cfg.ConnectRequest{Token: sign(fixtureSubject)}); err != nil {
			t.Fatal(err)
		}
	} else if !c.HandleCommand(&protocol.Command{Id: 1, Connect: &protocol.ConnectRequest{Token: sign(fixtureSubject)}}, 0) || c.UserID() != fixtureSubject {
		t.Fatal("native bidirectional connect failed")
	}
	if err := c.Subscribe("fixture"); err != nil {
		t.Fatal(err)
	}
	return n, c, q
}

func awaitPublicationSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(description)
	}
}

func waitPublicationCondition(t *testing.T, ready func() bool, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal(description)
		case <-time.After(time.Millisecond):
		}
	}
}

func queuePrivatePublications(t *testing.T, n *Node, c *cfg.Client, q *queuedTransport) {
	t.Helper()
	if err := c.Send([]byte(`{"fixture":"block-control"}`)); err != nil {
		t.Fatal(err)
	}
	awaitPublicationSignal(t, q.blocked, "control write not blocked")
	for _, data := range []string{`{"fixture":"private-publication"}`, `{"fixture":"caught-up-marker"}`} {
		if _, err := n.Underlying().Publish("fixture", []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNoPrivatePublications(t *testing.T, q *queuedTransport) {
	t.Helper()
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, b := range q.frames {
		if bytes.Contains(b, []byte("private-publication")) || bytes.Contains(b, []byte("caught-up-marker")) {
			t.Error("private publication or marker reached transport")
		}
	}
}

func decodePublicationPush(t *testing.T, wire publicationWire, data []byte) *protocol.Push {
	t.Helper()
	var push *protocol.Push
	var err error
	if wire.unidirectional {
		push = new(protocol.Push)
		if wire.protocol == cfg.ProtocolTypeProtobuf {
			err = push.UnmarshalCF(data)
		} else {
			err = json.Unmarshal(data, push)
		}
	} else {
		var reply protocol.Reply
		if wire.protocol == cfg.ProtocolTypeProtobuf {
			// Native event bytes are bare messages, not length-prefixed wire data.
			err = reply.UnmarshalCF(data)
		} else {
			err = json.Unmarshal(data, &reply)
		}
		push = reply.Push
	}
	if err != nil || push == nil || push.Pub == nil {
		t.Fatal("event is not one native publication", err)
	}
	return push
}

func TestPublicationEventPreservesNativeBytesAndSingleWrites(t *testing.T) {
	for _, wire := range publicationWires() {
		t.Run(wire.name, func(t *testing.T) {
			events := make(chan PublicationWriteEvent, 2)
			n, c, q := nativePublicationClient(t, t.Context(), wire, func(c Connection, e PublicationWriteEvent) bool {
				if c.UserID() != fixtureSubject || e.Channel != "fixture" || e.FrameType != protocol.FrameTypePushPublication || e.Protocol != wire.protocol || e.Unidirectional != wire.unidirectional {
					t.Error("native publication identity/metadata changed")
				}
				e.Data = bytes.Clone(e.Data) // Only the test-owned copy escapes.
				events <- e
				return true
			})
			queuePrivatePublications(t, n, c, q)
			q.release()
			for _, want := range []string{`{"fixture":"private-publication"}`, `{"fixture":"caught-up-marker"}`} {
				var event PublicationWriteEvent
				select {
				case event = <-events:
				case <-time.After(5 * time.Second):
					t.Fatal("native publication policy not called")
				}
				push := decodePublicationPush(t, wire, event.Data)
				if push.Channel != event.Channel || !bytes.Equal(push.Pub.Data, []byte(want)) || len(push.Pub.Tags) != 0 {
					t.Fatal("native envelope/payload changed or metadata injected")
				}
				waitPublicationCondition(t, func() bool {
					q.mu.Lock()
					defer q.mu.Unlock()
					return slices.ContainsFunc(q.frames, func(b []byte) bool { return bytes.Equal(b, event.Data) })
				}, "policy bytes differ from actual transport bytes")
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			if len(q.batchSizes) != 0 {
				t.Fatalf("normal policy writes aggregated: %v", q.batchSizes)
			}
		})
	}
}

func TestPublicationCloseFlushRejectsBeforePolicy(t *testing.T) {
	for _, wire := range publicationWires() {
		for _, advice := range []cfg.Disconnect{cfg.DisconnectPermissionDenied, cfg.DisconnectShutdown} {
			t.Run(wire.name+"/"+advice.Reason, func(t *testing.T) {
				var decisions atomic.Int64
				n, c, q := nativePublicationClient(t, t.Context(), wire, func(Connection, PublicationWriteEvent) bool {
					decisions.Add(1)
					return true
				})
				// The blocked control's I/O error stops the normal queue consumer
				// after release. The already-started PermissionDenied/Shutdown close
				// must drain the pending items through actual native WriteMany;
				// success here would otherwise race a normal single-item dequeue.
				q.blockError = io.ErrClosedPipe
				queuePrivatePublications(t, n, c, q)
				Connection{client: c}.Disconnect(advice)
				q.release()
				awaitPublicationSignal(t, q.closed, "native close flush deadlocked or transport not closed")
				if decisions.Load() != 0 {
					t.Errorf("unsubscribed close flush performed %d authority calls", decisions.Load())
				}
				assertNoPrivatePublications(t, q)
				q.mu.Lock()
				defer q.mu.Unlock()
				if len(q.batchSizes) == 0 {
					t.Fatal("fixture did not exercise native WriteMany close flush")
				}
			})
		}
	}
}

func TestPublicationRefusalLatchesQueuedFrames(t *testing.T) {
	for _, wire := range publicationWires() {
		t.Run(wire.name, func(t *testing.T) {
			var decisions atomic.Int64
			n, c, q := nativePublicationClient(t, t.Context(), wire, func(Connection, PublicationWriteEvent) bool {
				// Improving rights cannot revive the second queued frame/marker.
				return decisions.Add(1) > 1
			})
			queuePrivatePublications(t, n, c, q)
			q.release()
			awaitPublicationSignal(t, q.closed, "refusal did not disconnect without deadlock")
			if decisions.Load() != 1 {
				t.Fatalf("refusal did not latch: %d decisions", decisions.Load())
			}
			assertNoPrivatePublications(t, q)
		})
	}
}

func TestPublicationPostPolicyGuard(t *testing.T) {
	for _, wire := range publicationWires() {
		for _, mode := range []string{"close", "unsubscribe", "cancel", "stop"} {
			t.Run(wire.name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered, release := make(chan struct{}), make(chan struct{})
				enteredOnce := sync.OnceFunc(func() { close(entered) })
				var decisions atomic.Int64
				n, c, q := nativePublicationClient(t, ctx, wire, func(Connection, PublicationWriteEvent) bool {
					decisions.Add(1)
					enteredOnce()
					<-release
					return true
				})
				releaseOnce := sync.OnceFunc(func() { close(release) })
				t.Cleanup(releaseOnce)
				queuePrivatePublications(t, n, c, q)
				q.release()
				awaitPublicationSignal(t, entered, "native policy did not start")
				var done chan struct{}
				switch mode {
				case "close":
					Connection{client: c}.Disconnect(cfg.DisconnectPermissionDenied)
				case "unsubscribe":
					c.Unsubscribe("fixture")
				case "cancel":
					cancel()
				case "stop":
					done = make(chan struct{})
					go func() {
						if err := n.Stop(t.Context()); err != nil {
							t.Error(err)
						}
						close(done)
					}()
					waitPublicationCondition(t, func() bool { return !n.Ready() }, "native Stop did not seal readiness")
				}
				releaseOnce()
				if done != nil {
					awaitPublicationSignal(t, done, "close during policy deadlocked")
				}
				awaitPublicationSignal(t, q.closed, "post-policy failure did not disconnect")
				if decisions.Load() != 1 {
					t.Errorf("post-policy failure did not latch: %d decisions", decisions.Load())
				}
				assertNoPrivatePublications(t, q)
			})
		}
	}
}

func TestPublicationPolicyRequiresSingleMessageWrites(t *testing.T) {
	policy := WithPublicationWritePolicy(func(Connection, PublicationWriteEvent) bool { return true })
	for _, max := range []int{0, 1, 2} {
		opts := []Option{WithAnonymousConnectionsUnsafe(), policy}
		if max != 0 {
			opts = append(opts, WithMaxMessagesInFrame(max))
		}
		n, err := NewNode(opts...)
		if max > 1 {
			if err == nil || n != nil {
				t.Fatal("policy accepted multi-message writes")
			}
		} else if err != nil || n.maxMessagesInFrame != 1 {
			t.Fatal("policy did not select single-message writes", err)
		}
	}
	for _, max := range []int{0, 3} {
		opts := []Option{WithAnonymousConnectionsUnsafe()}
		if max != 0 {
			opts = append(opts, WithMaxMessagesInFrame(max))
		}
		n, err := NewNode(opts...)
		if err != nil || n.maxMessagesInFrame != max {
			t.Fatal("no-policy aggregation behavior changed", err)
		}
	}
}
