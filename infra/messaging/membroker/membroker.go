// Package membroker provides an in-memory message broker for unit tests.
// It implements the messaging.MessagePublisher interface and provides
// subscribe/dispatch capabilities without requiring a real RabbitMQ instance.
package membroker

import (
	"context"
	"strings"
	"sync"

	"github.com/bds421/rho-kit/infra/messaging"
)

// subscription pairs an exchange+routing key with a handler.
type subscription struct {
	exchange   string
	routingKey string
	handler    func(ctx context.Context, d messaging.Delivery) error
}

// Broker is an in-memory message broker for testing. It implements
// messaging.MessagePublisher and provides subscribe/drain capabilities
// for synchronous test-driven message processing.
type Broker struct {
	mu            sync.Mutex
	subscriptions []subscription
	published     []publishedMessage
}

type publishedMessage struct {
	exchange   string
	routingKey string
	msg        messaging.Message
}

// New creates a new in-memory broker.
func New() *Broker {
	return &Broker{}
}

// Publish stores a message and dispatches it to matching subscribers.
// Implements messaging.MessagePublisher.
func (b *Broker) Publish(_ context.Context, exchange, routingKey string, msg messaging.Message) error {
	b.mu.Lock()
	b.published = append(b.published, publishedMessage{
		exchange:   exchange,
		routingKey: routingKey,
		msg:        msg,
	})
	b.mu.Unlock()
	return nil
}

// Subscribe registers a handler for messages matching the given exchange
// and routing key. Use "*" for either to match all.
func (b *Broker) Subscribe(exchange, routingKey string, handler func(ctx context.Context, d messaging.Delivery) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscriptions = append(b.subscriptions, subscription{
		exchange:   exchange,
		routingKey: routingKey,
		handler:    handler,
	})
}

// Drain processes all pending published messages by dispatching them to
// matching subscribers synchronously. Returns the first error from any
// handler, or nil if all succeed.
func (b *Broker) Drain(ctx context.Context) error {
	b.mu.Lock()
	msgs := b.published
	b.published = nil
	subs := make([]subscription, len(b.subscriptions))
	copy(subs, b.subscriptions)
	b.mu.Unlock()

	for _, pm := range msgs {
		d := messaging.Delivery{
			Message:       pm.msg,
			Exchange:      pm.exchange,
			RoutingKey:    pm.routingKey,
			SchemaVersion: pm.msg.SchemaVersion,
		}
		for _, sub := range subs {
			if matches(sub, pm.exchange, pm.routingKey) {
				if err := sub.handler(ctx, d); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := make([]messaging.Message, len(b.published))
	for i, pm := range b.published {
		msgs[i] = pm.msg
	}
	return msgs
}

// Reset clears all published messages and subscriptions.
func (b *Broker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = nil
	b.subscriptions = nil
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
