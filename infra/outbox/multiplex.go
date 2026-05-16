package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrNoRoute is returned by [Multiplex.Publish] when no registered
// route prefix matches the entry's Topic and no default is set.
// Wave 149: the dispatcher fails closed by default so a misrouted
// entry is visible at publish time rather than silently dropped.
var ErrNoRoute = errors.New("outbox: no Multiplex route matches Entry.Topic")

// Multiplex routes outbox entries to one of several [Publisher]s
// based on the entry's Topic. Each registered route matches the
// longest topic prefix and delegates publishing to the bound
// publisher; entries that match no route fall through to a default
// publisher when one is configured, or fail with [ErrNoRoute].
//
// Wave 149 introduced this so the kit's outbox finally bridges
// transactional row writes to the messaging-backend layer without
// each service re-implementing "if topic startswith 'kafka.' use
// kafkabackend, else use amqpbackend". Concrete backend adapters
// (kit-shipped amqpbackend.OutboxPublisher etc., or service-defined)
// implement [Publisher] and register against this multiplexer.
//
// The zero value is not usable. Construct via [NewMultiplex].
//
// Multiplex is concurrency-safe — Publish and Register are
// safe to call from multiple goroutines, though registration is
// expected to happen at startup before any Publish call.
type Multiplex struct {
	mu       sync.RWMutex
	routes   map[string]Publisher
	fallback Publisher
}

// NewMultiplex constructs a Multiplex with no routes. Register
// per-prefix publishers via [Multiplex.Register] and optionally a
// fallback via [Multiplex.SetFallback].
func NewMultiplex() *Multiplex {
	return &Multiplex{routes: map[string]Publisher{}}
}

// Register binds a topic prefix to a publisher. The longest matching
// prefix wins at Publish time; an empty prefix is equivalent to
// SetFallback (and is rejected here so the intent is explicit).
//
// Panics if publisher is nil OR if prefix is empty — both are
// fail-fast wiring mistakes that would only surface as a confusing
// nil deref or silent override on the first Publish call.
//
// Re-registering an existing prefix replaces the bound publisher;
// the kit treats route registration as a startup-time write and
// does not preserve the previous binding.
func (m *Multiplex) Register(prefix string, publisher Publisher) {
	if prefix == "" {
		panic("outbox: Multiplex.Register requires a non-empty prefix; use SetFallback for the catch-all")
	}
	if publisher == nil {
		panic("outbox: Multiplex.Register requires a non-nil publisher")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[prefix] = publisher
}

// SetFallback installs a publisher used when no registered prefix
// matches an Entry's Topic. Passing nil clears the fallback (the
// multiplexer reverts to ErrNoRoute on unmatched entries).
func (m *Multiplex) SetFallback(publisher Publisher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallback = publisher
}

// Publish selects the longest-prefix-matching publisher for entry's
// Topic and delegates. Returns [ErrNoRoute] when no prefix matches
// and no fallback is set.
//
// On a downstream publish error, Multiplex wraps the inner error
// with the matched route prefix so the [outbox.Relay] retry
// metrics show which route is failing — important when a single
// service multiplexes across AMQP and Kafka and only one side is
// degraded.
func (m *Multiplex) Publish(ctx context.Context, entry Entry) error {
	publisher, matched := m.route(entry.Topic)
	if publisher == nil {
		return fmt.Errorf("%w: topic=%q", ErrNoRoute, entry.Topic)
	}
	if err := publisher.Publish(ctx, entry); err != nil {
		if matched != "" {
			return redact.WrapError(fmt.Sprintf("outbox/multiplex route %q", matched), err)
		}
		return redact.WrapError("outbox/multiplex fallback", err)
	}
	return nil
}

// route returns the publisher whose registered prefix is the longest
// proper prefix of topic. Falls back to the fallback publisher when
// no prefix matches. Returns ("", nil) when neither resolves.
func (m *Multiplex) route(topic string) (Publisher, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var bestPrefix string
	var bestPublisher Publisher
	for prefix, p := range m.routes {
		if !strings.HasPrefix(topic, prefix) {
			continue
		}
		if len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestPublisher = p
		}
	}
	if bestPublisher != nil {
		return bestPublisher, bestPrefix
	}
	return m.fallback, ""
}

// Compile-time interface check.
var _ Publisher = (*Multiplex)(nil)
