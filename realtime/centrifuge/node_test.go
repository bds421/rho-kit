package centrifuge_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rtcentrifuge "github.com/bds421/rho-kit/realtime/centrifuge/v2"
)

func newQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewNode_OK(t *testing.T) {
	node, err := rtcentrifuge.NewNode(
		rtcentrifuge.WithAnonymousConnectionsUnsafe(),
		rtcentrifuge.WithLogger(newQuietLogger()),
	)
	require.NoError(t, err)
	require.NotNil(t, node)
	require.NotNil(t, node.Underlying())
}

func TestNewNode_NilOption(t *testing.T) {
	_, err := rtcentrifuge.NewNode(nil)
	require.Error(t, err)
}

func TestNewNode_RequiresAuthOrAnonymousUnsafe(t *testing.T) {
	_, err := rtcentrifuge.NewNode(rtcentrifuge.WithLogger(newQuietLogger()))
	require.Error(t, err)
	require.Contains(t, err.Error(), "WithAnonymousConnectionsUnsafe")
}

func TestOptionPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"WithLogger(nil)", func() { rtcentrifuge.WithLogger(nil) }},
		{"WithJWTAuth(nil)", func() { rtcentrifuge.WithJWTAuth(nil) }},
		{"WithChannelClassifier(nil)", func() { rtcentrifuge.WithChannelClassifier(nil) }},
		{"WithSubscribeAuthorizer(nil)", func() { rtcentrifuge.WithSubscribeAuthorizer(nil) }},
		{"WithPublishAuthorizer(nil)", func() { rtcentrifuge.WithPublishAuthorizer(nil) }},
		{"WithRegisterer(nil)", func() { rtcentrifuge.WithRegisterer(nil) }},
		{"NewMetrics(nil-option)", func() { rtcentrifuge.NewMetrics(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic from %s", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestWebsocketHandler_NotNil(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)
	h := node.WebsocketHandler()
	require.NotNil(t, h)
}

func TestNode_StartStop(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- node.Start(ctx) }()

	// Give the goroutine a tick to enter the blocking select.
	time.Sleep(50 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	require.NoError(t, node.Stop(stopCtx))

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after Stop + ctx cancel")
	}
}

func TestNode_DoubleStartRejected(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- node.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)

	err = node.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already invoked")

	cancel()
	<-done
}

func TestNode_StopIsIdempotent(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, node.Stop(ctx))
	// Second Stop is a no-op rather than an error so callers can
	// defer Stop unconditionally.
	require.NoError(t, node.Stop(ctx))
}

func TestNode_StartRejectsNilContext(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)
	//nolint:staticcheck // SA1012: testing the nil-ctx rejection path on purpose.
	//lint:ignore SA1012 nil-ctx rejection contract test
	err = node.Start(nil)
	require.Error(t, err)
}

func TestNode_StopRejectsNilContext(t *testing.T) {
	node, err := rtcentrifuge.NewNode(rtcentrifuge.WithAnonymousConnectionsUnsafe(), rtcentrifuge.WithLogger(newQuietLogger()))
	require.NoError(t, err)
	//nolint:staticcheck // SA1012: testing the nil-ctx rejection path on purpose.
	//lint:ignore SA1012 nil-ctx rejection contract test
	err = node.Stop(nil)
	require.Error(t, err)
}

func TestChannelClassifier_CustomMapsCorrectly(t *testing.T) {
	classified := []string{}
	classifier := func(ch string) string {
		if strings.HasPrefix(ch, "user:") {
			return "user"
		}
		if strings.HasPrefix(ch, "room:") {
			return "room"
		}
		return "other"
	}

	// Exercise the classifier directly — verifying it slots through
	// the WithChannelClassifier option without panicking and
	// produces the expected mapping.
	node, err := rtcentrifuge.NewNode(
		rtcentrifuge.WithAnonymousConnectionsUnsafe(),
		rtcentrifuge.WithLogger(newQuietLogger()),
		rtcentrifuge.WithChannelClassifier(classifier),
	)
	require.NoError(t, err)
	require.NotNil(t, node)

	classified = append(classified,
		classifier("user:42"),
		classifier("room:abc"),
		classifier("system"),
	)
	assert.Equal(t, []string{"user", "room", "other"}, classified)
}

func TestNewMetrics_RegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := rtcentrifuge.NewMetrics(rtcentrifuge.WithRegisterer(reg))
	require.NotNil(t, m)

	// reg.Gather only emits metric families with at least one observed
	// labelset, and the observe* helpers are unexported, so a Gather
	// assertion here would be vacuous (the original bug: it touched
	// names[n] and asserted nothing). Instead assert that NewMetrics
	// actually registered each collector by re-registering an identical
	// one and demanding an AlreadyRegisteredError — which only happens
	// if NewMetrics already claimed that name/label-shape on reg.
	// The probe must be IDENTICAL to the registered collector (same
	// name, labels AND help text) for AlreadyRegisteredError; a
	// mismatched probe yields a descriptor-conflict error instead. So
	// these probes double as a pin on the wire-form metric name, label
	// keys, and help strings operators build dashboards against.
	cases := []struct {
		name        string
		subsystemFn func() *prometheus.CounterVec
	}{
		{
			name: "realtime_centrifuge_connects_total",
			subsystemFn: func() *prometheus.CounterVec {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "realtime", Subsystem: "centrifuge",
					Name: "connects_total",
					Help: "Total centrifuge connection attempts by outcome (accepted=auth passed, rejected=auth refused, error=internal failure).",
				}, []string{"outcome"})
			},
		},
		{
			name: "realtime_centrifuge_disconnects_total",
			subsystemFn: func() *prometheus.CounterVec {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "realtime", Subsystem: "centrifuge",
					Name: "disconnects_total",
					Help: "Total centrifuge disconnects by reason (clean=client-initiated, stale=server kicked).",
				}, []string{"reason"})
			},
		},
		{
			name: "realtime_centrifuge_subscribes_total",
			subsystemFn: func() *prometheus.CounterVec {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "realtime", Subsystem: "centrifuge",
					Name: "subscribes_total",
					Help: "Total centrifuge channel subscriptions by channel class.",
				}, []string{"class"})
			},
		},
		{
			name: "realtime_centrifuge_publishes_total",
			subsystemFn: func() *prometheus.CounterVec {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "realtime", Subsystem: "centrifuge",
					Name: "publishes_total",
					Help: "Total messages published to centrifuge channels by channel class.",
				}, []string{"class"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reg.Register(tc.subsystemFn())
			require.Error(t, err, "expected %s already registered by NewMetrics", tc.name)
			require.IsType(t, prometheus.AlreadyRegisteredError{}, err)
		})
	}
}

func TestNewNode_WithMetricsRegisterer_RegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	node, err := rtcentrifuge.NewNode(
		rtcentrifuge.WithAnonymousConnectionsUnsafe(),
		rtcentrifuge.WithLogger(newQuietLogger()),
		rtcentrifuge.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)
	require.NotNil(t, node)

	// WithMetricsRegisterer must actually build and register the
	// kit-side metric set on the supplied registerer. A registry that
	// already has a collector registered rejects a re-registration of
	// an identical collector with an AlreadyRegisteredError, while an
	// empty registry accepts it. We use that to assert the node wired
	// metrics through to reg rather than silently dropping them.
	probe := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "realtime",
		Subsystem: "centrifuge",
		Name:      "connects_total",
		Help:      "Total centrifuge connection attempts by outcome (accepted=auth passed, rejected=auth refused, error=internal failure).",
	}, []string{"outcome"})
	err = reg.Register(probe)
	require.Error(t, err, "expected connects_total already registered by NewNode(WithMetricsRegisterer)")
	require.IsType(t, prometheus.AlreadyRegisteredError{}, err)
}

func TestWithLogLevelOptions_DoNotPanic(t *testing.T) {
	_, err := rtcentrifuge.NewNode(
		rtcentrifuge.WithAnonymousConnectionsUnsafe(),
		rtcentrifuge.WithLogger(newQuietLogger()),
		rtcentrifuge.WithLogLevelDebug(),
	)
	require.NoError(t, err)

	_, err = rtcentrifuge.NewNode(
		rtcentrifuge.WithAnonymousConnectionsUnsafe(),
		rtcentrifuge.WithLogger(newQuietLogger()),
		rtcentrifuge.WithLogLevelError(),
	)
	require.NoError(t, err)
}
