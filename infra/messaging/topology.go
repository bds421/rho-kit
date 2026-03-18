package messaging

import (
	"fmt"
	"time"
)

// ExchangeKind constants for declaring exchanges.
const (
	ExchangeDirect  = "direct"
	ExchangeFanout  = "fanout"
	ExchangeTopic   = "topic"
	ExchangeHeaders = "headers"
)

// RetryPolicy configures retry behavior for a binding.
// MaxRetries is the number of retry attempts (total attempts = MaxRetries + 1).
// Delay is the wait time before a failed message is re-delivered.
type RetryPolicy struct {
	MaxRetries int
	Delay      time.Duration
}

// BindingSpec describes an exchange, queue, and the routing key that connects them.
// It is the configuration input for backend-specific topology declaration.
type BindingSpec struct {
	Exchange     string
	ExchangeType string
	Queue        string
	RoutingKey   string
	Retry        *RetryPolicy
}

// Binding is a BindingSpec whose topology has been declared on the broker.
// When Retry is set, it includes pre-computed names for the retry and dead
// infrastructure, eliminating naming-convention logic at the consumer level.
type Binding struct {
	BindingSpec
	RetryExchange string // "" when Retry is nil
	RetryQueue    string
	DeadExchange  string
	DeadQueue     string
}

// ExchangeSpec describes an exchange to declare without any queue binding.
// Used for publisher-only exchanges where consumers are not yet defined.
type ExchangeSpec struct {
	Exchange     string
	ExchangeType string
}

// ValidateBindingSpecs checks that all specs have valid exchange names, queue
// names, exchange types, routing keys, and retry policies.
func ValidateBindingSpecs(specs []BindingSpec) error {
	for _, b := range specs {
		if b.Exchange == "" {
			return fmt.Errorf("exchange name must not be empty")
		}
		if b.Queue == "" {
			return fmt.Errorf("queue name must not be empty")
		}
		switch b.ExchangeType {
		case ExchangeDirect, ExchangeFanout, ExchangeTopic, ExchangeHeaders:
		default:
			return fmt.Errorf("unsupported exchange type: %q", b.ExchangeType)
		}
		if b.RoutingKey == "" && (b.ExchangeType == ExchangeDirect || b.ExchangeType == ExchangeTopic) {
			return fmt.Errorf("routing key required for %s exchange %q", b.ExchangeType, b.Exchange)
		}
		if b.Retry != nil {
			if b.Retry.MaxRetries < 1 {
				return fmt.Errorf("binding %q: RetryPolicy.MaxRetries must be >= 1", b.Queue)
			}
			if b.Retry.Delay <= 0 {
				return fmt.Errorf("binding %q: RetryPolicy.Delay must be > 0", b.Queue)
			}
		}
	}
	return nil
}

// ComputeBindings converts BindingSpecs into Bindings by computing the
// retry/dead exchange and queue names. Unlike backend-specific DeclareAll,
// it requires no broker connection — it is a pure function. Consumer services
// use this to obtain Binding objects without declaring topology themselves.
func ComputeBindings(specs ...BindingSpec) ([]Binding, error) {
	if err := ValidateBindingSpecs(specs); err != nil {
		return nil, err
	}

	result := make([]Binding, 0, len(specs))
	for _, b := range specs {
		db := Binding{BindingSpec: b}
		if b.Retry != nil {
			db.RetryExchange = b.Exchange + ".retry"
			db.RetryQueue = b.Queue + ".retry"
			db.DeadExchange = DeadExchangeName(b.Exchange)
			db.DeadQueue = b.Queue + ".dead"
		}
		result = append(result, db)
	}
	return result, nil
}

// FindBinding returns the first Binding whose RoutingKey matches the given key.
// It returns an error if no match is found, ensuring configuration drift is
// caught at startup rather than at runtime.
func FindBinding(bindings []Binding, routingKey string) (Binding, error) {
	for _, b := range bindings {
		if b.RoutingKey == routingKey {
			return b, nil
		}
	}
	return Binding{}, fmt.Errorf("no binding found for routing key %q", routingKey)
}

// DeadExchangeName returns the conventional dead-letter exchange name.
func DeadExchangeName(exchange string) string { return exchange + ".dead" }
