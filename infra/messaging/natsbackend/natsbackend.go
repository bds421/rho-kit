// Package natsbackend implements a NATS JetStream-backed
// [messaging.Publisher] and consumer.
//
// JetStream gives the kit:
//
//   - Persistence — messages survive a broker restart.
//   - Acknowledgements — Publish returns only after the broker has
//     accepted+stored the message.
//   - Pull consumers with explicit ack — durable consumer state
//     tracks per-message ack status across restarts.
//
// Use this backend when:
//   - You need higher throughput than single-node RabbitMQ can deliver.
//   - You don't want the operational overhead of Kafka.
//   - Your consumers can tolerate at-least-once delivery semantics
//     (deduplicate at the application layer if exactly-once is needed).
//
// The translation between [messaging.Message] and NATS JetStream:
//
//   - Stream subject = `encode(exchange) + "." + encode(routingKey)`
//     when routingKey is non-empty, otherwise just `encode(exchange)`.
//     [composeSubject] URL-encodes NATS-reserved characters (`.`, `*`,
//     `>`, whitespace) within each token so that a dotted exchange
//     name like `orders.v1` cannot widen a wildcard subscription. The
//     unencoded segment boundary between exchange and routingKey is
//     preserved, keeping NATS-native wildcards (e.g. `orders.v1.>`)
//     workable for operators that align their stream subjects with the
//     kit's encoding scheme.
//   - The original (unencoded) exchange and routing-key are carried as
//     NATS message headers (`X-Exchange`, `X-Routing-Key`). The
//     consumer reads these headers to reconstruct the
//     [messaging.Delivery], which preserves the original pair exactly
//     even when one or both contain reserved characters. Subjects are
//     only used as a fallback for messages from non-kit publishers
//     (see [splitSubject]).
//   - Message body = JSON-encoded [messaging.Message] (same shape used
//     by the AMQP and Redis backends).
//   - User headers ride the NATS Msg.Header map.
package natsbackend

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/tlsclone"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

const (
	headerExchange   = "X-Exchange"
	headerRoutingKey = "X-Routing-Key"
)

// closeDrainTimeout caps how long [Connection.Close] waits for a graceful
// drain. Beyond this we force-close so an unhealthy broker or stuck pending
// publish cannot stall shutdown indefinitely.
const closeDrainTimeout = 5 * time.Second

const minimumTLSVersion = tls.VersionTLS12

// maxConsumerDeliveryBytes caps the JetStream-delivered message bytes
// the consumer will hand to json.Unmarshal. NATS's broker-side
// max_msg_size is the first defence, but a misconfigured stream or
// a foreign-writer scenario could still surface oversized messages —
// this cap stops a parse-cost OOM at the kit boundary. 32 MiB matches
// the kit's general AMQP/NATS upper bound (mirrors amqpbackend.maxConsumerDeliveryBytes).
const maxConsumerDeliveryBytes = 32 * 1024 * 1024

// Config is the connection-level configuration. Stream/consumer
// declarations live on [StreamConfig] and [ConsumerConfig] so a single
// connection can serve multiple streams.
type Config struct {
	URL string // e.g. "nats://localhost:4222"

	// Name identifies this client in NATS introspection. Defaults to
	// "rho-kit".
	Name string

	// PublishAckWait caps how long a synchronous Publish waits for the
	// JetStream broker ack. Default: 5s.
	PublishAckWait time.Duration

	// MaxReconnects bounds reconnection attempts before NATS gives up.
	// -1 means infinite. Default: -1.
	MaxReconnects int

	// ReconnectWait is the back-off between reconnect attempts.
	// Default: 2s.
	ReconnectWait time.Duration

	// TLS, when non-nil, configures TLS for the connection. Provide a
	// fully-formed *tls.Config — the typical kit construction is
	// netutil.TLSConfig{...}.ClientTLS(). If TLS is nil but the URL
	// scheme is "tls" or "wss", the NATS client uses its own default
	// system trust store.
	TLS *tls.Config

	// Username and Password configure plaintext NATS user/password
	// authentication. Use either these or Token / Credentials / NKey,
	// not multiple at once.
	Username string
	Password string

	// UsernamePasswordProvider supplies user/password credentials
	// whenever nats.go authenticates or reauthenticates a connection.
	// Use this for rotating broker credentials. The ctx carries the
	// per-call deadline derived from [CredentialProviderTimeout] (default
	// 5s); providers that hit a remote secret manager MUST honour it.
	//
	// Returned errors are logged and a previous successfully-resolved
	// credential pair is reused if available — i.e. transient secret
	// manager outages do not break an already-connected NATS client.
	// On the first attempt with no cached value, an error fails the
	// auth handler and triggers a reconnect cycle.
	UsernamePasswordProvider func(ctx context.Context) (user, password string, err error)

	// Token configures NATS bearer-token authentication.
	Token string

	// TokenProvider supplies a bearer token whenever nats.go authenticates
	// or reauthenticates a connection. Use this for rotating tokens; it
	// takes precedence over Token when both are set. The ctx + caching
	// semantics match [UsernamePasswordProvider].
	TokenProvider func(ctx context.Context) (string, error)

	// CredentialProviderTimeout bounds each call into [UsernamePasswordProvider]
	// or [TokenProvider]. Zero (the default) uses 5 seconds; values below
	// 100ms are clamped up to 100ms so providers always have a usable
	// budget. The kit derives a per-call ctx with this timeout so
	// providers that hit a remote secret manager cannot hang the NATS
	// auth handler.
	CredentialProviderTimeout time.Duration

	// CredentialsFile points to a NATS `.creds` JWT-NKey credentials
	// file (the standard format produced by `nsc add user`). Honoured
	// when non-empty.
	CredentialsFile string

	// NKeyFile points to a seed file for NKey authentication. Honoured
	// when non-empty. Takes precedence over Username/Password/Token if
	// multiple are set, matching the nats.go option order.
	NKeyFile string

	// AllowInsecure opts the connection out of the FR-073 production
	// safety check. Without TLS or any auth method (Username, Token,
	// CredentialsFile, NKeyFile), [Connect] refuses to dial because a
	// plaintext+no-auth NATS connection on a multi-tenant network is
	// effectively unauthenticated message ingress. Set this to true
	// for genuinely trusted single-host development setups, or any
	// case where the operator has independently confirmed the NATS
	// server requires no authentication and runs on a private
	// network.
	AllowInsecure bool

	// ExtraOptions are raw nats.Option values appended after the
	// kit-derived options. The escape hatch covers anything the typed
	// fields do not — custom error handlers, custom dialers,
	// per-message inbox prefixes, etc. Applied last so callers can
	// override defaults the kit installs.
	ExtraOptions []nats.Option
}

// Clone returns a detached copy of cfg suitable for storing past the caller's
// setup phase. TLS config is cloned and raised to the kit's TLS floor; option
// slices are copied so later caller mutation cannot change runtime wiring.
func (c Config) Clone() (Config, error) {
	c.ExtraOptions = append([]nats.Option(nil), c.ExtraOptions...)
	if c.TLS != nil {
		tlsConfig, err := cloneTLSConfigWithFloor(c.TLS)
		if err != nil {
			return Config{}, err
		}
		c.TLS = tlsConfig
	}
	return c, nil
}

// LogValue implements slog.LogValuer to prevent accidental logging of
// credentials or topology if a caller constructs a Config with URL-embedded
// userinfo or deployment-specific hosts.
func (c Config) LogValue() slog.Value {
	urlValid, urlHostConfigured, urlUserConfigured, urlPasswordConfigured := natsURLLogState(c.URL)
	return slog.GroupValue(
		slog.Bool("url_configured", c.URL != ""),
		slog.Bool("url_valid", urlValid),
		slog.Bool("host_configured", urlHostConfigured),
		slog.Bool("name_configured", c.Name != ""),
		slog.Bool("tls_configured", c.TLS != nil),
		slog.Bool("username_configured", c.Username != "" || urlUserConfigured),
		slog.Bool("password_configured", c.Password != "" || urlPasswordConfigured),
		slog.Bool("username_password_provider_configured", c.UsernamePasswordProvider != nil),
		slog.Bool("token_configured", c.Token != ""),
		slog.Bool("token_provider_configured", c.TokenProvider != nil),
		slog.Bool("credentials_file_configured", c.CredentialsFile != ""),
		slog.Bool("nkey_file_configured", c.NKeyFile != ""),
		slog.Bool("allow_insecure", c.AllowInsecure),
	)
}

func natsURLLogState(rawURL string) (valid, hostConfigured, userConfigured, passwordConfigured bool) {
	if rawURL == "" {
		return true, false, false, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, false, false, false
	}
	if u.User != nil {
		_, passwordConfigured = u.User.Password()
	}
	return true, u.Host != "", u.User != nil && u.User.Username() != "", passwordConfigured
}

// ValidateURL checks that rawURL is a NATS server URL with an explicit host.
// Credentials, query parameters, and fragments are rejected; pass auth through
// Config's typed fields so logs can redact consistently.
func ValidateURL(rawURL string) error {
	_, err := parseServerURL(rawURL)
	return err
}

func parseServerURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("natsbackend: URL must not be empty")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("natsbackend: URL is invalid")
	}
	switch strings.ToLower(u.Scheme) {
	case "nats", "tls", "ws", "wss":
	default:
		return nil, fmt.Errorf("natsbackend: URL scheme must be nats, tls, ws, or wss")
	}
	if err := config.ValidateURLHost("natsbackend: URL", u); err != nil {
		return nil, err
	}
	if u.User != nil {
		return nil, errors.New("natsbackend: URL must not contain credentials; use Config.Username, Token, CredentialsFile, or NKeyFile")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("natsbackend: URL must not contain query or fragment components")
	}
	return u, nil
}

// validateAuth enforces the FR-073 safety contract: a NATS connection
// must use TLS, some form of authentication, or AllowInsecure. The
// check runs before any DNS or socket activity so a misconfigured
// service fails fast at startup rather than silently shipping
// unauthenticated traffic.
func (c Config) validateAuth(serverURL *url.URL) error {
	if c.AllowInsecure {
		return nil
	}
	if c.TLS != nil {
		return nil
	}
	if c.Username != "" || c.Token != "" || c.CredentialsFile != "" || c.NKeyFile != "" ||
		c.UsernamePasswordProvider != nil || c.TokenProvider != nil {
		return nil
	}
	if serverURL != nil {
		switch strings.ToLower(serverURL.Scheme) {
		case "tls", "wss":
			return nil
		}
	}
	if strings.HasPrefix(strings.ToLower(c.URL), "tls://") || strings.HasPrefix(strings.ToLower(c.URL), "wss://") {
		// URL scheme requests TLS even when no *tls.Config is
		// supplied; nats.go falls back to the system trust store. The
		// connection is still encrypted, so we accept this as the
		// "TLS is configured" signal.
		return nil
	}
	return errors.New("natsbackend: connect requires TLS, authentication (Username/Token/CredentialsFile/NKeyFile or provider), or explicit AllowInsecure (audit FR-073)")
}

func cloneTLSConfigWithFloor(cfg *tls.Config) (*tls.Config, error) {
	// Hot-rotation configs (from security/netutil.ReloadingClientTLS)
	// intentionally set InsecureSkipVerify=true and replace stdlib
	// verification with VerifyConnection so they can validate against
	// the freshest CA pool on every handshake. Permit the opt-in
	// explicitly when VerifyConnection is non-nil — without this,
	// callers cannot use the Builder's reloading TLS source with NATS
	// and must choose between rotation and the kit's anti-downgrade
	// guardrail.
	cloneOpts := []tlsclone.Option(nil)
	if cfg != nil && cfg.InsecureSkipVerify && cfg.VerifyConnection != nil {
		cloneOpts = append(cloneOpts, tlsclone.AllowInsecureSkipVerify())
	}
	cloned, err := tlsclone.ConfigWithFloor(cfg, minimumTLSVersion, cloneOpts...)
	if err != nil {
		if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
			return nil, errors.New("natsbackend: TLS InsecureSkipVerify=true is not permitted")
		}
		return nil, errors.New("natsbackend: TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned, nil
}

// Connection holds an open nats.Conn and its JetStream context. Use
// [Connect] to construct.
type Connection struct {
	nc             *nats.Conn
	js             jetstream.JetStream
	publishAckWait time.Duration
}

// Connect dials NATS and returns a Connection. The connection
// auto-reconnects on transient failures; callers do not need to wrap
// it in a retry loop.
//
// Audit FR-073 [HIGH]: Connect refuses to dial a plaintext NATS
// endpoint with no authentication. Configure TLS, Username/Password,
// Token, CredentialsFile, or NKeyFile — or set AllowInsecure for
// genuinely trusted single-host setups.
func Connect(ctx context.Context, cfg Config) (*Connection, error) {
	if ctx == nil {
		return nil, errors.New("natsbackend: Connect requires a non-nil context")
	}
	var err error
	cfg, err = cfg.Clone()
	if err != nil {
		return nil, err
	}
	serverURL, err := parseServerURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if err := cfg.validateAuth(serverURL); err != nil {
		return nil, err
	}
	if cfg.PublishAckWait < 0 {
		return nil, errors.New("natsbackend: PublishAckWait must not be negative")
	}
	if cfg.MaxReconnects < -1 {
		return nil, errors.New("natsbackend: MaxReconnects must be >= -1")
	}
	if cfg.ReconnectWait < 0 {
		return nil, errors.New("natsbackend: ReconnectWait must not be negative")
	}
	if cfg.Name == "" {
		cfg.Name = "rho-kit"
	}
	if cfg.MaxReconnects == 0 {
		cfg.MaxReconnects = -1 // infinite
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = 2 * time.Second
	}

	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(cfg.ReconnectWait),
		// Bound the internal drain step so a stuck broker cannot stall
		// shutdown beyond closeDrainTimeout. The outer wrapper in Close()
		// also force-closes if the goroutine itself does not return.
		nats.DrainTimeout(closeDrainTimeout),
	}
	if cfg.TLS != nil {
		opts = append(opts, nats.Secure(cfg.TLS))
	}
	timeout := cfg.CredentialProviderTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	} else if timeout < 100*time.Millisecond {
		timeout = 100 * time.Millisecond
	}
	if cfg.UsernamePasswordProvider != nil {
		opts = append(opts, nats.UserInfoHandler(newUserPassBridge(cfg.UsernamePasswordProvider, timeout)))
	} else if cfg.Username != "" {
		opts = append(opts, nats.UserInfo(cfg.Username, cfg.Password))
	}
	if cfg.TokenProvider != nil {
		opts = append(opts, nats.TokenHandler(newTokenBridge(cfg.TokenProvider, timeout)))
	} else if cfg.Token != "" {
		opts = append(opts, nats.Token(cfg.Token))
	}
	if cfg.CredentialsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredentialsFile))
	}
	if cfg.NKeyFile != "" {
		nkeyOpt, err := nats.NkeyOptionFromSeed(cfg.NKeyFile)
		if err != nil {
			return nil, fmt.Errorf("natsbackend: load NKey seed: %w", err)
		}
		opts = append(opts, nkeyOpt)
	}
	// ExtraOptions are appended BEFORE the security-critical re-apply
	// so callers can extend behaviour (custom error handlers, custom
	// dialers, inbox prefixes) but cannot disable kit-hardened
	// defaults. Wave 66 closed a hostile-review finding that
	// ExtraOptions could override TLS, the drain bound, and the
	// reconnect cap by being appended last.
	opts = append(opts, cfg.ExtraOptions...)
	opts = append(opts, nats.DrainTimeout(closeDrainTimeout))
	if cfg.TLS != nil {
		opts = append(opts, nats.Secure(cfg.TLS))
	}
	opts = append(opts, nats.MaxReconnects(cfg.MaxReconnects))
	// Honour ctx deadline for the dial. nats.Connect itself does not
	// accept a context, so we derive a finite Timeout from the deadline
	// when present. Without this, a cancelled ctx would not abort the
	// dial.
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 {
			opts = append(opts, nats.Timeout(d))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("natsbackend: connect: %w", err)
	}

	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("natsbackend: connect: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("natsbackend: jetstream: %w", err)
	}
	return &Connection{nc: nc, js: js, publishAckWait: cfg.PublishAckWait}, nil
}

// Healthy reports whether the underlying NATS connection is currently
// connected. Suitable for [messaging.Connector].
func (c *Connection) Healthy() bool {
	if c == nil {
		return false
	}
	return c.nc != nil && c.nc.IsConnected()
}

// Stop drains pending publishes and closes the connection. Drain is
// best-effort: the ctx deadline (or [closeDrainTimeout] when ctx has
// none) bounds the wait — if drain does not finish in time we
// force-close so an unhealthy broker cannot stall shutdown.
func (c *Connection) Stop(ctx context.Context) error {
	if c == nil || c.nc == nil {
		return nil
	}
	timeout := closeDrainTimeout
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			c.nc.Close()
			return err
		}
		if dl, ok := ctx.Deadline(); ok {
			if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
				timeout = remaining
			}
		}
	}
	return drainWithTimeout(c.nc.Drain, c.nc.Close, timeout)
}

// drainWithTimeout runs drain in a goroutine and force-closes via close if
// drain has not returned within timeout. Extracted so unit tests can
// substitute fakes for the underlying nats.Conn methods.
func drainWithTimeout(drain func() error, close func(), timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- drain() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		close()
		return fmt.Errorf("natsbackend: drain exceeded %s, force-closed", timeout)
	}
}

// JetStream returns the raw JetStream context for callers needing
// features the kit doesn't expose. Use sparingly.
func (c *Connection) JetStream() jetstream.JetStream {
	if c == nil {
		return nil
	}
	return c.js
}

// StreamConfig declares a JetStream stream's persistence policy.
type StreamConfig struct {
	Name        string
	Subjects    []string // e.g. ["events.>"]
	MaxAge      time.Duration
	MaxBytes    int64
	Retention   jetstream.RetentionPolicy // default: LimitsPolicy
	StorageType jetstream.StorageType     // default: FileStorage
}

// EnsureStream creates or updates the stream described by cfg.
// Idempotent — safe to call on every startup.
func (c *Connection) EnsureStream(ctx context.Context, cfg StreamConfig) error {
	if c == nil || c.js == nil {
		return errors.New("natsbackend: connection is not initialized")
	}
	if cfg.Name == "" {
		return errors.New("natsbackend: StreamConfig.Name required")
	}
	if len(cfg.Subjects) == 0 {
		return errors.New("natsbackend: StreamConfig.Subjects required")
	}
	jcfg := jetstream.StreamConfig{
		Name:      cfg.Name,
		Subjects:  cfg.Subjects,
		MaxAge:    cfg.MaxAge,
		MaxBytes:  cfg.MaxBytes,
		Retention: cfg.Retention,
		Storage:   cfg.StorageType,
	}
	_, err := c.js.CreateOrUpdateStream(ctx, jcfg)
	if err != nil {
		return fmt.Errorf("natsbackend: ensure stream: %w", err)
	}
	return nil
}

// Publisher publishes [messaging.Message]s to JetStream.
type Publisher struct {
	conn        *Connection
	wait        time.Duration
	sizeLimiter messaging.MessageSizeLimiter
	metrics     *Metrics
}

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithPublishAckWait overrides the per-publish ack-wait timeout. The duration
// must be positive; use [WithoutPublishAckWait] to inherit only the caller's
// context deadline.
func WithPublishAckWait(d time.Duration) PublisherOption {
	if d <= 0 {
		panic("natsbackend: WithPublishAckWait requires a positive duration")
	}
	return func(p *Publisher) { p.wait = d }
}

// WithoutPublishAckWait disables the publisher-level ack-wait timeout.
// Use only when callers always provide a bounded context.
func WithoutPublishAckWait() PublisherOption {
	return func(p *Publisher) { p.wait = 0 }
}

// WithMessageSizeLimiter replaces the publisher's message-size policy.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) PublisherOption {
	return func(p *Publisher) { p.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// limits configured with WithRouteMaxMessageBytes still apply.
func WithoutMaxMessageBytes() PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one exact
// exchange+routing-key pair. routingKey may be empty for fanout-style routes.
func WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
	}
}

// WithPublisherMetrics attaches Prometheus metrics to the publisher.
func WithPublisherMetrics(m *Metrics) PublisherOption {
	if m == nil {
		panic("natsbackend: WithPublisherMetrics requires non-nil metrics")
	}
	return func(p *Publisher) { p.metrics = m }
}

// defaultPublishAckWait is used when neither [Config.PublishAckWait] nor
// [WithPublishAckWait] is set.
const defaultPublishAckWait = 5 * time.Second

// NewPublisher returns a Publisher backed by conn. The publish ack-wait
// defaults to [defaultPublishAckWait]; callers can override it with
// [WithPublishAckWait], or surface it through [Connection.NewPublisher]
// which threads [Config.PublishAckWait] automatically.
func NewPublisher(conn *Connection, opts ...PublisherOption) *Publisher {
	if conn == nil {
		panic("natsbackend: Publisher requires a Connection")
	}
	p := &Publisher{
		conn:        conn,
		wait:        defaultPublishAckWait,
		sizeLimiter: messaging.DefaultMessageSizeLimiter(),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("natsbackend: Publisher option must not be nil")
		}
		opt(p)
	}
	return p
}

// NewPublisher returns a Publisher backed by this Connection, threading
// the connection's [Config.PublishAckWait] (when non-zero) through to the
// publisher. Use this rather than the package-level [NewPublisher] when
// you want operator-tuned ack-wait behavior. Additional [PublisherOption]
// values override the threaded default.
func (c *Connection) NewPublisher(opts ...PublisherOption) *Publisher {
	if c == nil {
		return NewPublisher(nil, opts...)
	}
	all := make([]PublisherOption, 0, len(opts)+1)
	if c.publishAckWait > 0 {
		all = append(all, WithPublishAckWait(c.publishAckWait))
	}
	all = append(all, opts...)
	return NewPublisher(c, all...)
}

func (p *Publisher) ready() error {
	if p == nil || p.conn == nil || p.conn.js == nil {
		return messaging.ErrInvalidPublisher
	}
	return nil
}

// Publish satisfies [messaging.Publisher].
//
// The NATS subject is the sanitized form of `exchange + "." + routingKey`
// (or just the sanitized `exchange` when routingKey is empty). Dots
// within an exchange or routing-key segment are URL-encoded so the
// boundary is unambiguous. The original (unencoded) values also ride as
// `X-Exchange` / `X-Routing-Key` headers, which the consumer prefers
// when reconstructing the [messaging.Delivery]. Returns only after the
// JetStream broker confirms storage, so a non-nil return guarantees the
// message will not be lost to a broker crash.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	started := time.Now()
	outcome := natsPublishOutcomeFailed
	defer func() {
		p.metrics.observePublish(exchange, routingKey, outcome, started)
	}()
	msg = msg.Clone()
	subject := composeSubject(exchange, routingKey)
	if subject == "" {
		return errors.New("natsbackend: composed subject is empty")
	}
	if err := messaging.ValidateMessage(msg); err != nil {
		outcome = natsPublishOutcomeInvalidMessage
		return err
	}
	if err := p.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		outcome = publishOutcomeForError(err)
		return err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natsbackend: marshal message: %w", err)
	}
	natsMsg := &nats.Msg{
		Subject: subject,
		Data:    body,
		Header:  nats.Header{},
	}
	for k, v := range msg.Headers {
		natsMsg.Header.Set(k, v)
	}
	natsMsg.Header.Set("X-Message-Id", msg.ID)
	natsMsg.Header.Set("X-Message-Type", msg.Type)
	natsMsg.Header.Set(headerExchange, exchange)
	natsMsg.Header.Set(headerRoutingKey, routingKey)

	pubCtx := ctx
	if p.wait > 0 {
		var cancel context.CancelFunc
		pubCtx, cancel = context.WithTimeout(ctx, p.wait)
		defer cancel()
	}
	_, err = p.conn.js.PublishMsg(pubCtx, natsMsg)
	if err != nil {
		return fmt.Errorf("natsbackend: publish: %w", err)
	}
	outcome = natsPublishOutcomeSuccess
	return nil
}

// ConsumerConfig declares a durable JetStream consumer. The kit
// represents one consumer per (stream, durable name) tuple — the
// durable name pins consumer position across restarts.
type ConsumerConfig struct {
	Stream        string
	Durable       string
	FilterSubject string        // optional — restrict to a subject within the stream
	MaxAckPending int           // default: 256
	AckWait       time.Duration // default: 30s
	// MaxDeliver caps how many times JetStream will redeliver a single
	// message before giving up. Without a cap (the JetStream default of
	// -1 meaning unlimited), a message that reliably triggers a panic
	// in the handler — or any other non-Term failure — Naks forever and
	// blocks the consumer's progress. Default: 5. Set negative to
	// explicitly opt into unlimited redelivery.
	MaxDeliver int
}

// Consumer pulls messages from a JetStream durable consumer and
// dispatches them to a handler. One Consumer instance per
// (stream, durable).
type Consumer struct {
	conn    *Connection
	cfg     ConsumerConfig
	logger  *slog.Logger
	metrics *Metrics
}

// ConsumerOption configures a Consumer.
type ConsumerOption func(*Consumer)

// WithConsumerMetrics attaches Prometheus metrics to the consumer.
func WithConsumerMetrics(m *Metrics) ConsumerOption {
	if m == nil {
		panic("natsbackend: WithConsumerMetrics requires non-nil metrics")
	}
	return func(c *Consumer) { c.metrics = m }
}

// NewConsumer constructs a Consumer. The underlying durable consumer
// is created lazily on the first [Consumer.Consume] call so callers
// don't pay the round trip during DI wiring.
func NewConsumer(conn *Connection, cfg ConsumerConfig, logger *slog.Logger, opts ...ConsumerOption) *Consumer {
	if conn == nil {
		panic("natsbackend: Consumer requires a Connection")
	}
	if cfg.Stream == "" || cfg.Durable == "" {
		panic("natsbackend: ConsumerConfig requires Stream and Durable")
	}
	if cfg.MaxAckPending < 0 {
		panic("natsbackend: ConsumerConfig.MaxAckPending must be >= 0")
	}
	if cfg.AckWait < 0 {
		panic("natsbackend: ConsumerConfig.AckWait must not be negative")
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 256
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver == 0 {
		cfg.MaxDeliver = 5
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &Consumer{conn: conn, cfg: cfg, logger: logger}
	for _, opt := range opts {
		if opt == nil {
			panic("natsbackend: Consumer option must not be nil")
		}
		opt(c)
	}
	return c
}

func (c *Consumer) ready() error {
	if c == nil ||
		c.conn == nil ||
		c.conn.js == nil ||
		c.logger == nil ||
		c.cfg.Stream == "" ||
		c.cfg.Durable == "" ||
		c.cfg.MaxAckPending < 0 ||
		c.cfg.AckWait < 0 {
		return messaging.ErrInvalidConsumer
	}
	return nil
}

// Consume blocks until ctx cancels, dispatching messages to handler.
// Returning nil from handler acks; returning an error nacks (the
// message is redelivered after AckWait).
func (c *Consumer) Consume(ctx context.Context, handler messaging.Handler) error {
	if err := c.ready(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("natsbackend: handler must not be nil")
	}

	cons, err := c.conn.js.CreateOrUpdateConsumer(ctx, c.cfg.Stream, jetstream.ConsumerConfig{
		Durable:       c.cfg.Durable,
		FilterSubject: c.cfg.FilterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: c.cfg.MaxAckPending,
		AckWait:       c.cfg.AckWait,
		MaxDeliver:    c.cfg.MaxDeliver,
	})
	if err != nil {
		return fmt.Errorf("natsbackend: ensure consumer: %w", err)
	}

	var stopped atomic.Bool
	consume, err := cons.Consume(func(jm jetstream.Msg) {
		if stopped.Load() {
			return
		}
		c.dispatch(ctx, jm, handler)
	})
	if err != nil {
		return fmt.Errorf("natsbackend: start consume: %w", err)
	}

	<-ctx.Done()
	stopped.Store(true)
	consume.Stop()
	return nil
}

// dispatch routes one delivery to the handler and ack/nacks based on
// the result. A handler panic counts as an error — the message is
// nacked so it redelivers up to ConsumerConfig.MaxDeliver times, after
// which JetStream gives up (sends to the configured DLQ if any, else
// drops). The panic is recovered here and not re-raised; the consumer
// must keep running so other deliveries continue to be processed.
// Process-level reaction to handler panics should subscribe to the
// "natsbackend: handler panicked" log line, not rely on a re-throw.
func (c *Consumer) dispatch(ctx context.Context, jm jetstream.Msg, handler messaging.Handler) {
	var handlerStarted time.Time
	var handlerStartedSet bool
	defer func() {
		if r := recover(); r != nil {
			if handlerStartedSet {
				c.metrics.observeHandler(c.cfg.Stream, c.cfg.Durable, natsHandlerOutcomePanic, handlerStarted)
			}
			// Term, not Nak: a panic is a poison-pill — re-running the
			// same payload through the same handler will panic again,
			// burning the entire MaxDeliver budget for nothing. Term
			// hands it straight to the JetStream DLQ.
			if err := jm.Term(); err != nil {
				c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeTermFailed)
			} else {
				c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeHandlerPanic)
			}
			c.logger.Error("natsbackend: handler panicked — terminating message",
				redact.Panic(r),
			)
		}
	}()

	subject := jm.Subject()
	exchange, routingKey := extractExchangeAndRoutingKey(jm)

	// Cap delivery bytes before json.Unmarshal so a JetStream-side
	// configuration that produces unusually large messages cannot OOM
	// the consumer at parse time. NATS's own max_msg_size operates at
	// the broker; this is the kit-side safety net.
	if data := jm.Data(); len(data) > maxConsumerDeliveryBytes {
		c.logger.Error("natsbackend: oversized message — discarding",
			redact.String("subject", subject),
			"size_bytes", len(data),
		)
		if err := jm.Term(); err != nil {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeTermFailed)
		} else {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeDecodeError)
		}
		return
	}
	var msg messaging.Message
	if err := json.Unmarshal(jm.Data(), &msg); err != nil {
		c.logger.Error("natsbackend: malformed message — discarding",
			redact.String("subject", subject),
			redact.Error(err),
		)
		if err := jm.Term(); err != nil {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeTermFailed)
		} else {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeDecodeError)
		}
		return
	}

	headers, msgHeaders := deliveryHeaderMaps(jm.Headers())
	msg = msg.Clone()
	msg.Headers = msgHeaders

	// Validate the post-extraction message contract before invoking the
	// handler. Publishers call messaging.ValidateMessage before sending,
	// but a foreign producer (or a misconfigured peer) can bypass the kit
	// publisher path entirely. Without this guard, handlers receive
	// metadata the messaging.Message contract says cannot exist —
	// oversized IDs/types, too many headers, invalid header bytes, etc.
	// Term rather than Nak: a malformed message will never validate, so
	// JetStream redelivery would burn the entire MaxDeliver budget.
	if err := messaging.ValidateMessage(msg); err != nil {
		c.logger.Error("natsbackend: inbound message failed validation — terminating",
			redact.String("subject", subject),
			redact.String("msg_id", msg.ID),
			redact.Error(err),
		)
		if err := jm.Term(); err != nil {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeTermFailed)
		} else {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeValidateError)
		}
		return
	}

	// Surface JetStream-level redelivery via metadata.
	redelivered := false
	if md, err := jm.Metadata(); err == nil {
		redelivered = md.NumDelivered > 1
	}

	delivery := messaging.Delivery{
		Message:       msg.Clone(),
		Exchange:      exchange,
		RoutingKey:    routingKey,
		SchemaVersion: msg.SchemaVersion,
		Redelivered:   redelivered,
		Headers:       headers,
	}

	handlerStarted = time.Now()
	handlerStartedSet = true
	if err := handler(ctx, delivery); err != nil {
		c.metrics.observeHandler(c.cfg.Stream, c.cfg.Durable, natsHandlerOutcomeError, handlerStarted)
		// Permanent errors (apperror.PermanentError) get Term'd so
		// JetStream stops redelivering immediately, mirroring the
		// AMQP backend's poison-pill handling. Without this every
		// permanent failure burns the entire MaxDeliver budget.
		if apperror.IsPermanent(err) {
			c.logger.Error("natsbackend: permanent error — terminating message",
				redact.String("subject", subject),
				redact.String("msg_id", msg.ID),
				redact.Error(err),
			)
			if err := jm.Term(); err != nil {
				c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeTermFailed)
			} else {
				c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomePermanent)
			}
			return
		}
		c.logger.Warn("natsbackend: handler returned error — nacking",
			redact.String("subject", subject),
			redact.String("msg_id", msg.ID),
			redact.Error(err),
		)
		if err := jm.Nak(); err != nil {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeNakFailed)
		} else {
			c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeRetry)
		}
		return
	}
	c.metrics.observeHandler(c.cfg.Stream, c.cfg.Durable, natsHandlerOutcomeSuccess, handlerStarted)
	if err := jm.Ack(); err != nil {
		c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeAckFailed)
		return
	}
	c.metrics.observeConsumed(c.cfg.Stream, c.cfg.Durable, natsConsumeOutcomeAcked)
}

func publishOutcomeForError(err error) string {
	if errors.Is(err, messaging.ErrMessageTooLarge) {
		return natsPublishOutcomeTooLarge
	}
	return natsPublishOutcomeFailed
}

// maxNatsDeliveryHeaders caps the number of headers materialised from a
// JetStream delivery so a hostile publisher cannot force unbounded
// allocations upfront. Mirrors the AMQP maxHeaderNodes cap.
const maxNatsDeliveryHeaders = 256

func deliveryHeaderMaps(h nats.Header) (map[string]any, map[string]string) {
	if len(h) == 0 {
		return nil, nil
	}
	headers := make(map[string]any)
	msgHeaders := make(map[string]string)
	for k, v := range h {
		if len(headers) >= maxNatsDeliveryHeaders {
			break
		}
		if len(v) > 0 {
			headers[k] = v[0]
			msgHeaders[k] = v[0]
		}
	}
	if len(headers) == 0 {
		headers = nil
	}
	if len(msgHeaders) == 0 {
		msgHeaders = nil
	}
	return headers, msgHeaders
}

// composeSubject builds the NATS subject for an (exchange, routingKey)
// pair. Each token is run through [encodeSubjectToken], which percent-
// escapes NATS-reserved characters (`.`, `*`, `>`, whitespace), and the
// two encoded tokens are joined with a literal `.` so the segment
// boundary is unambiguous (audit FR-074). A dotted exchange like
// `billing.invoices` therefore cannot collide with a wildcard
// subscription targeting `billing.>` — the dot inside the exchange is
// encoded to `%2E`. Reconstruction of the original (exchange,
// routingKey) on the consumer side reads the `X-Exchange` /
// `X-Routing-Key` headers — never the subject — so the round-trip
// preserves the unencoded values regardless of which characters they
// contain.
func composeSubject(exchange, routingKey string) string {
	if routingKey == "" {
		return encodeSubjectToken(exchange)
	}
	return encodeSubjectToken(exchange) + "." + encodeSubjectToken(routingKey)
}

// encodeSubjectToken URL-encodes characters that NATS reserves —
// '.', '*', '>', whitespace — within a single subject token.
func encodeSubjectToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '.':
			b.WriteString("%2E")
		case '*':
			b.WriteString("%2A")
		case '>':
			b.WriteString("%3E")
		case ' ', '\t', '\r', '\n':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitSubject is the legacy splitter used only for messages that
// arrive without the [headerExchange] / [headerRoutingKey] headers
// (e.g. produced by older clients or tools other than this backend).
// It splits on the first dot, which is lossy for dotted exchange names
// but matches pre-v2 behaviour. The dispatcher prefers headers.
func splitSubject(subject string) (exchange, routingKey string) {
	i := strings.IndexByte(subject, '.')
	if i < 0 {
		return subject, ""
	}
	return subject[:i], subject[i+1:]
}

// extractExchangeAndRoutingKey returns the exchange and routing-key for
// a delivery. It prefers the [headerExchange] / [headerRoutingKey]
// headers (added by [Publisher.Publish]) and falls back to splitting
// the subject for messages produced by older clients.
func extractExchangeAndRoutingKey(jm jetstream.Msg) (exchange, routingKey string) {
	hdr := jm.Headers()
	if hdr != nil {
		ex := hdr.Get(headerExchange)
		rk := hdr.Get(headerRoutingKey)
		if ex != "" || rk != "" {
			return ex, rk
		}
	}
	return splitSubject(jm.Subject())
}
