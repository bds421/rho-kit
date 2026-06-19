// Package membroker provides an in-memory message broker for unit tests.
// It implements the messaging.Publisher interface and provides
// subscribe/dispatch capabilities without requiring a real RabbitMQ instance.
package membroker

import (
	"context"
	"strings"
	"sync"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// SubscriptionID identifies a subscription returned by [Broker.Subscribe]
// so callers can later [Broker.Unsubscribe] without resetting the broker.
type SubscriptionID uint64

// subscription pairs an exchange+routing key with a handler.
type subscription struct {
	id         SubscriptionID
	exchange   string
	routingKey string
	handler    func(ctx context.Context, d messaging.Delivery) error
}

// Broker is an in-memory message broker for testing. It implements
// messaging.Publisher and provides subscribe/drain capabilities
// for synchronous test-driven message processing.
type Broker struct {
	mu            sync.Mutex
	subscriptions []subscription
	nextID        SubscriptionID
	nextPublishID uint64
	published     []publishedMessage
	sizeLimiter   messaging.MessageSizeLimiter
	drainMu       sync.Mutex
}

type publishedMessage struct {
	id         uint64
	exchange   string
	routingKey string
	msg        messaging.Message
}

// Option configures an in-memory Broker.
type Option func(*Broker)

// WithMessageSizeLimiter replaces the broker's message-size policy.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) Option {
	return func(b *Broker) { b.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) Option {
	return func(b *Broker) {
		b.sizeLimiter = b.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// limits configured with WithRouteMaxMessageBytes still apply.
func WithoutMaxMessageBytes() Option {
	return func(b *Broker) {
		b.sizeLimiter = b.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one exact
// exchange+routing-key pair. routingKey may be empty for fanout-style routes.
func WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) Option {
	return func(b *Broker) {
		b.sizeLimiter = b.sizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
	}
}

// New creates a new in-memory broker.
func New(opts ...Option) *Broker {
	b := &Broker{sizeLimiter: messaging.DefaultMessageSizeLimiter()}
	for _, opt := range opts {
		if opt == nil {
			panic("membroker: New option must not be nil")
		}
		opt(b)
	}
	return b
}

func (b *Broker) ready() error {
	if b == nil {
		return messaging.ErrInvalidPublisher
	}
	return nil
}

// Publish stores a message and dispatches it to matching subscribers.
// Implements messaging.Publisher.
func (b *Broker) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := b.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	if err := messaging.ValidateMessage(msg); err != nil {
		return err
	}
	if err := b.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		return err
	}
	b.mu.Lock()
	b.nextPublishID++
	b.published = append(b.published, publishedMessage{
		id:         b.nextPublishID,
		exchange:   exchange,
		routingKey: routingKey,
		msg:        msg.Clone(),
	})
	b.mu.Unlock()
	return nil
}

// Subscribe registers a handler for messages matching the given exchange
// and routing key. Routing-key matching follows AMQP 0-9-1 topic rules:
//
//   - "#" matches zero or more dot-delimited segments (the catch-all).
//   - "*" matches exactly one segment, so a "*" pattern will NOT match a
//     multi-segment key like "user.created" — use "#" or a positional
//     pattern such as "user.*".
//   - The exchange argument also accepts "*" or "#" as a wildcard meaning
//     "any exchange"; otherwise it is validated as a literal exchange name.
//
// Returns a [SubscriptionID] that callers can pass to [Broker.Unsubscribe]
// for fine-grained teardown without dropping unrelated subscriptions.
//
// Panics if b or handler is nil, or the exchange/routing-key
// arguments fail [messaging.ValidateExchangeName] /
// [messaging.ValidateRoutingKey]. These are wiring mistakes that
// should fail at startup, not silently route nothing.
func (b *Broker) Subscribe(exchange, routingKey string, handler func(ctx context.Context, d messaging.Delivery) error) SubscriptionID {
	if b == nil {
		panic("membroker: Subscribe requires a non-nil Broker")
	}
	if handler == nil {
		panic("membroker: Subscribe requires a non-nil handler")
	}
	if exchange != "*" && exchange != "#" {
		if err := messaging.ValidateExchangeName(exchange); err != nil {
			panic("membroker: Subscribe exchange name is invalid")
		}
	}
	if err := messaging.ValidateRoutingKey(routingKey); err != nil {
		panic("membroker: Subscribe routing key is invalid")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	b.subscriptions = append(b.subscriptions, subscription{
		id:         id,
		exchange:   exchange,
		routingKey: routingKey,
		handler:    handler,
	})
	return id
}

// Unsubscribe removes the subscription with the given id. Silently ignores
// unknown ids so callers can call Unsubscribe in deferred test cleanup
// without checking whether the subscription is still registered. Use this
// instead of [Reset] when only one of several subscriptions should go.
func (b *Broker) Unsubscribe(id SubscriptionID) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subscriptions {
		if sub.id == id {
			b.subscriptions = append(b.subscriptions[:i], b.subscriptions[i+1:]...)
			return
		}
	}
}

// Drain processes all pending published messages by dispatching them to
// matching subscribers synchronously. Returns the first error from any
// handler, or nil if all succeed.
func (b *Broker) Drain(ctx context.Context) error {
	if err := b.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	// Serialize Drain so concurrent callers (e.g. parallel PublishAndDrain)
	// cannot peek the same head entry before either removes it, which would
	// dispatch the same message to subscribers more than once. b.mu is still
	// released during handler dispatch so handlers may publish or subscribe.
	b.drainMu.Lock()
	defer b.drainMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		if len(b.published) == 0 {
			b.mu.Unlock()
			return nil
		}
		pm := b.published[0]
		subs := make([]subscription, len(b.subscriptions))
		copy(subs, b.subscriptions)
		b.mu.Unlock()

		for _, sub := range subs {
			if matches(sub, pm.exchange, pm.routingKey) {
				if err := ctx.Err(); err != nil {
					return err
				}
				d := messaging.Delivery{
					Message:       pm.msg.Clone(),
					Exchange:      pm.exchange,
					RoutingKey:    pm.routingKey,
					SchemaVersion: pm.msg.SchemaVersion,
				}
				if err := sub.handler(ctx, d); err != nil {
					return err
				}
			}
		}

		b.mu.Lock()
		if len(b.published) > 0 && b.published[0].id == pm.id {
			b.published = b.published[1:]
		} else {
			b.removePublishedLocked(pm.id)
		}
		b.mu.Unlock()
	}
}

// PublishAndDrain publishes a message and immediately dispatches it to matching
// subscribers synchronously. This is a convenience for tests that want
// immediate dispatch semantics similar to real AMQP brokers.
func (b *Broker) PublishAndDrain(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := b.Publish(ctx, exchange, routingKey, msg); err != nil {
		return err
	}
	return b.Drain(ctx)
}

// Published returns all messages that have been published since the last
// Drain or Reset. Useful for asserting in tests.
func (b *Broker) Published() []messaging.Message {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := make([]messaging.Message, len(b.published))
	for i, pm := range b.published {
		msgs[i] = pm.msg.Clone()
	}
	return msgs
}

// Reset clears all published messages and subscriptions.
func (b *Broker) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = nil
	b.subscriptions = nil
}

func (b *Broker) removePublishedLocked(id uint64) {
	for i, candidate := range b.published {
		if candidate.id == id {
			b.published = append(b.published[:i], b.published[i+1:]...)
			return
		}
	}
}

// matches checks whether a subscription matches the given exchange and routing key.
// Supports AMQP topic-exchange patterns:
//   - "*" matches exactly one dot-delimited word
//   - "#" matches zero or more dot-delimited words
//
// Examples: "orders.*" matches "orders.created" but not "orders.items.created".
// "orders.#" matches "orders", "orders.created", and "orders.items.created".
func matches(sub subscription, exchange, routingKey string) bool {
	if sub.exchange != "*" && sub.exchange != "#" && sub.exchange != exchange {
		return false
	}
	return matchTopic(sub.routingKey, routingKey)
}

// matchTopic implements AMQP 0-9-1 topic matching between a pattern and a routing key.
func matchTopic(pattern, key string) bool {
	if pattern == "#" || (pattern == "*" && !strings.Contains(key, ".")) {
		return true
	}
	if pattern == key {
		return true
	}

	patternParts := strings.Split(pattern, ".")
	keyParts := strings.Split(key, ".")

	return matchParts(patternParts, keyParts)
}

func matchParts(pattern, key []string) bool {
	pi, ki := 0, 0
	for pi < len(pattern) && ki < len(key) {
		switch pattern[pi] {
		case "#":
			// "#" at end matches everything remaining.
			if pi == len(pattern)-1 {
				return true
			}
			// Try matching "#" against 0..N key words.
			for skip := ki; skip <= len(key); skip++ {
				if matchParts(pattern[pi+1:], key[skip:]) {
					return true
				}
			}
			return false
		case "*":
			// "*" matches exactly one word.
			pi++
			ki++
		default:
			if pattern[pi] != key[ki] {
				return false
			}
			pi++
			ki++
		}
	}
	// Remaining pattern parts must all be "#" to match empty remainder.
	for pi < len(pattern) {
		if pattern[pi] != "#" {
			return false
		}
		pi++
	}
	return ki == len(key)
}
