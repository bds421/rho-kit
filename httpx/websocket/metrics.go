package websocket

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Direction labels for the WebSocket message metrics.
const (
	directionIn  = "in"
	directionOut = "out"
)

// Metrics holds the Prometheus collectors for the WebSocket adapter.
type Metrics struct {
	active        prometheus.Gauge
	messages      *prometheus.CounterVec
	messageBytes  *prometheus.HistogramVec
	closes        *prometheus.CounterVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for the
// WebSocket metric set. Unset defaults to
// [prometheus.DefaultRegisterer]; passing nil panics so a miswired
// caller surfaces at startup rather than going to the global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("httpx/websocket: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics constructs the WebSocket metric set. Pass [WithRegisterer]
// to use a non-default registry. Repeated calls reuse already-registered
// collectors on the same registry so duplicate construction across
// servers is safe.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("httpx/websocket: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "httpx",
			Subsystem: "websocket",
			Name:      "active",
			Help:      "Number of currently-open WebSocket connections served by the kit adapter.",
		}),
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "httpx",
			Subsystem: "websocket",
			Name:      "messages_total",
			Help:      "Total WebSocket messages exchanged by direction (in=read from peer, out=written to peer).",
		}, []string{"direction"}),
		messageBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "httpx",
			Subsystem: "websocket",
			Name:      "message_bytes",
			Help:      "WebSocket message payload size in bytes by direction.",
			Buckets:   []float64{64, 256, 1024, 4096, 16 * 1024, 64 * 1024, 256 * 1024, 1024 * 1024},
		}, []string{"direction"}),
		closes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "httpx",
			Subsystem: "websocket",
			Name:      "close_total",
			Help:      "Total WebSocket close handshakes by normalised close code.",
		}, []string{"code"}),
	}

	m.active = promutil.MustRegisterOrGet(reg, m.active)
	m.messages = promutil.MustRegisterOrGet(reg, m.messages)
	m.messageBytes = promutil.MustRegisterOrGet(reg, m.messageBytes)
	m.closes = promutil.MustRegisterOrGet(reg, m.closes)
	return m
}

func (m *Metrics) connOpened() {
	if m == nil {
		return
	}
	m.active.Inc()
}

func (m *Metrics) connClosed(code int) {
	if m == nil {
		return
	}
	m.active.Dec()
	m.closes.WithLabelValues(closeCodeLabel(code)).Inc()
}

func (m *Metrics) observeMessage(direction string, size int) {
	if m == nil {
		return
	}
	m.messages.WithLabelValues(direction).Inc()
	m.messageBytes.WithLabelValues(direction).Observe(float64(size))
}

// closeCodeLabel normalises a WebSocket close code into a bounded
// Prometheus label. RFC 6455 specifies a small set of standard codes
// (1000-1015); the 3000-4999 range is reserved for application use.
// Standard codes are emitted as their decimal string; everything else
// flows through [promutil.OpaqueLabelValue] so per-tenant or
// attacker-controlled codes cannot blow up cardinality.
func closeCodeLabel(code int) string {
	if code >= 1000 && code <= 1015 {
		return strconv.Itoa(code)
	}
	if code == 0 {
		return "unknown"
	}
	return promutil.OpaqueLabelValue("code", strconv.Itoa(code))
}
