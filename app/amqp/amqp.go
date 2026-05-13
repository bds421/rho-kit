package amqp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// Resource keys under which the Module publishes its connection,
// publisher, and consumer handles. Retrieve them via [Connection],
// [Publisher], [Consumer].
const (
	ResourceConnectionKey = "github.com/bds421/rho-kit/app/amqp.connection"
	ResourcePublisherKey  = "github.com/bds421/rho-kit/app/amqp.publisher"
	ResourceConsumerKey   = "github.com/bds421/rho-kit/app/amqp.consumer"
)

// Option configures the AMQP [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	criticalBroker     bool
	messageSizeLimiter messaging.MessageSizeLimiter
	urlProvider        func(context.Context) (string, error)
	allowPlaintext     bool
}

// WithCriticalBroker makes the broker health check critical (503 on failure).
// By default the broker health is non-critical (degraded, not unhealthy) so
// transient broker hiccups don't cascade into a service-wide unready state.
func WithCriticalBroker() Option {
	return func(c *moduleConfig) {
		c.criticalBroker = true
	}
}

// WithURLProvider configures a dynamic URL source. The provider is evaluated
// before each AMQP dial/reconnect, so rotated broker passwords are picked up
// without rebuilding the service. The static URL passed to [Module] is
// ignored when a provider is supplied.
func WithURLProvider(provider func(context.Context) (string, error)) Option {
	if provider == nil {
		panic("amqp: WithURLProvider requires a non-nil provider")
	}
	return func(c *moduleConfig) {
		c.urlProvider = provider
	}
}

// WithMessageSizeLimiter installs a serialized-message-size limiter. The
// limiter caps inbound and outbound message bodies; routes can override the
// default via [messaging.MessageSizeLimiter.WithRouteMaxBytes].
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) Option {
	return func(c *moduleConfig) {
		c.messageSizeLimiter = l
	}
}

// WithoutTLS opts out of the AMQP transport-safety check that [Module]
// applies. Without this opt-in, [Module] panics at construction time when
// the URL scheme is `amqp://` and the host does not resolve to loopback —
// plaintext credentials on the wire would otherwise reach a routable
// broker.
//
// Use this only for local-development fixtures where the broker is
// confirmed to be unreachable from outside the host (Docker host-only
// network, ephemeral sidecar). The check is unconditional otherwise —
// there is no KIT_ENV escape hatch.
func WithoutTLS() Option {
	return func(c *moduleConfig) {
		c.allowPlaintext = true
	}
}

// Module returns an [app.Module] that opens and supervises a RabbitMQ
// connection plus a default publisher and consumer. Pass to
// [app.Builder.With].
//
// Transport safety: a non-loopback `amqp://` URL panics at construction
// time unless [WithoutTLS] is passed. Use `amqps://` (or [WithoutTLS] for
// loopback dev) to bypass the check.
//
// Panics if url is empty AND no [WithURLProvider] option is supplied.
func Module(amqpURL string, opts ...Option) app.Module {
	mc := moduleConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("amqp: Module option must not be nil")
		}
		opt(&mc)
	}
	if amqpURL == "" && mc.urlProvider == nil {
		panic("amqp: Module requires a non-empty URL or WithURLProvider")
	}
	if amqpURL != "" && !mc.allowPlaintext {
		enforceTransportSafety(amqpURL)
	}
	return &messagingModule{
		url:                amqpURL,
		urlProvider:        mc.urlProvider,
		criticalBroker:     mc.criticalBroker,
		messageSizeLimiter: mc.messageSizeLimiter,
	}
}

// messagingModule implements the Module interface for RabbitMQ connections.
// It handles connection setup, publisher/consumer creation, health checks,
// and cleanup.
type messagingModule struct {
	url            string
	urlProvider    func(context.Context) (string, error)
	criticalBroker bool

	// initialized during Init
	conn      *amqpbackend.Connection
	publisher *amqpbackend.Publisher
	consumer  messaging.Consumer
	logger    *slog.Logger

	messageSizeLimiter messaging.MessageSizeLimiter
}

func (m *messagingModule) Name() string { return "rabbitmq" }

func (m *messagingModule) Init(_ context.Context, mc app.ModuleContext) error {
	m.logger = mc.Logger

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("amqp module: build client TLS: %w", err)
	}

	// Construct metrics up front so the Dial path can observe connection_up
	// and reconnect attempts. The same Metrics instance is shared with the
	// publisher and consumer so a single registry sees publish/consume +
	// connection-lifecycle samples without name collisions.
	metrics := amqpbackend.NewMetrics(nil)

	mqOpts := []amqpbackend.DialOption{
		amqpbackend.WithLazyConnect(),
		amqpbackend.WithConnectionMetrics(metrics, "default"),
	}
	if clientTLS != nil {
		mqOpts = append(mqOpts, amqpbackend.WithTLS(clientTLS))
	}
	if m.urlProvider != nil {
		mqOpts = append(mqOpts, amqpbackend.WithURLProvider(m.urlProvider))
	}

	conn, dialErr := amqpbackend.Connect(m.url, mc.Logger, mqOpts...)
	if dialErr != nil {
		return fmt.Errorf("amqp module: %w", dialErr)
	}
	m.conn = conn

	pub := amqpbackend.NewPublisher(conn, mc.Logger,
		amqpbackend.WithMessageSizeLimiter(m.messageSizeLimiter),
		amqpbackend.WithPublisherMetrics(metrics),
	)
	m.publisher = pub
	m.consumer = amqpbackend.NewConsumer(conn, pub, mc.Logger, amqpbackend.WithConsumerMetrics(metrics))

	mc.Logger.Info("rabbitmq connection configured")
	return nil
}

func (m *messagingModule) HealthChecks() []health.DependencyCheck {
	if m.conn == nil {
		return nil
	}
	if m.criticalBroker {
		return []health.DependencyCheck{amqpbackend.CriticalHealthCheck(m.conn)}
	}
	return []health.DependencyCheck{amqpbackend.HealthCheck(m.conn)}
}

func (m *messagingModule) Populate(infra *app.Infrastructure) {
	if m.conn != nil {
		infra.SetResource(ResourceConnectionKey, m.conn)
	}
	if m.publisher != nil {
		infra.SetResource(ResourcePublisherKey, m.publisher)
	}
	if m.consumer != nil {
		infra.SetResource(ResourceConsumerKey, m.consumer)
	}
}

func (m *messagingModule) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.publisher != nil {
		m.publisher.Close()
		m.publisher = nil
	}
	if m.conn == nil {
		return nil
	}
	conn := m.conn
	m.conn = nil
	if err := conn.Stop(ctx); err != nil {
		m.logger.Warn("error closing rabbitmq", slog.Any("error", err))
		return err
	}
	return nil
}

// enforceTransportSafety panics when amqpURL targets a non-loopback broker
// over plaintext `amqp://`. Local-dev fixtures (loopback) bypass the check;
// production deployments must use `amqps://` or explicitly opt out with
// [WithoutTLS].
func enforceTransportSafety(amqpURL string) {
	u, err := url.Parse(amqpURL)
	if err != nil {
		// Defer detailed URL parsing to amqpbackend.Connect; we only
		// guard the obvious plaintext-on-non-loopback case here.
		return
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "amqp" {
		return // amqps:// is TLS; nothing to enforce.
	}
	host := u.Hostname()
	if host == "" {
		return // amqpbackend.Connect will reject the empty host.
	}
	if isLoopbackHost(host) {
		return
	}
	panic("amqp: Module requires amqps:// for non-loopback hosts (use WithoutTLS for local dev)")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Connection returns the AMQP connection published by [Module] under
// [ResourceConnectionKey], or nil if no amqp adapter was registered with
// the Builder.
func Connection(infra app.Infrastructure) messaging.Connector {
	v, ok := infra.Resource(ResourceConnectionKey)
	if !ok {
		return nil
	}
	c, _ := v.(messaging.Connector)
	return c
}

// Publisher returns the AMQP publisher published by [Module] under
// [ResourcePublisherKey], or nil if no amqp adapter was registered with
// the Builder.
func Publisher(infra app.Infrastructure) messaging.Publisher {
	v, ok := infra.Resource(ResourcePublisherKey)
	if !ok {
		return nil
	}
	p, _ := v.(messaging.Publisher)
	return p
}

// Consumer returns the AMQP consumer published by [Module] under
// [ResourceConsumerKey], or nil if no amqp adapter was registered with
// the Builder.
func Consumer(infra app.Infrastructure) messaging.Consumer {
	v, ok := infra.Resource(ResourceConsumerKey)
	if !ok {
		return nil
	}
	c, _ := v.(messaging.Consumer)
	return c
}
