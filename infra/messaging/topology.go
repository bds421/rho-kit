package messaging

import (
	"errors"
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

// DefaultRetryPolicy returns the kit's default retry policy applied to
// bindings that omit Retry and do not set WithoutRetry. Three retries
// at 10-second intervals matches typical consumer-side transient-error
// recovery (database connection bounce, AMQP reconnect grace period,
// downstream API rate-limit cooldown).
func DefaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{MaxRetries: 3, Delay: 10 * time.Second}
}

// BindingSpec describes an exchange, queue, and the routing key that connects them.
// It is the configuration input for backend-specific topology declaration.
//
// Retry behaviour:
//   - Retry set: that policy is used as-is.
//   - Retry nil + WithoutRetry false: [DefaultRetryPolicy] is applied at
//     [ValidateBindingSpecs] time and a warning is logged. This is the
//     safe default — silent ack-and-discard on first transient error
//     (the previous default) was a footgun.
//   - WithoutRetry true: ack-and-discard on the first handler error,
//     with no retry topology declared. Use only for fire-and-forget
//     workloads (broadcast notifications, idempotent deletes, etc.)
//     where message loss on transient failures is acceptable.
//
// Setting both Retry and WithoutRetry is a configuration error and
// fails [ValidateBindingSpecs].
type BindingSpec struct {
	Exchange     string
	ExchangeType string
	// ConsumerGroup is the identity of the cooperating-consumers group
	// that delivers this binding. In AMQP this is also the queue name
	// (queue ↔ consumer group are 1:1 in AMQP). In Kafka/Redis Streams
	// it is the consumer-group identity passed to the broker — the
	// stream/topic itself comes from Exchange.
	//
	// Renamed from Queue in v2.0.0 wave 155 to drop the AMQP-ism.
	ConsumerGroup string
	RoutingKey    string
	Retry         *RetryPolicy
	WithoutRetry  bool
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

// CloneBindingSpecs returns a detached copy of specs. Retry policies are
// copied so returned bindings can be stored past setup without retaining
// caller-owned mutable config.
func CloneBindingSpecs(specs []BindingSpec) []BindingSpec {
	if len(specs) == 0 {
		return nil
	}
	cloned := make([]BindingSpec, len(specs))
	for i, spec := range specs {
		cloned[i] = cloneBindingSpec(spec)
	}
	return cloned
}

func cloneBindingSpec(spec BindingSpec) BindingSpec {
	if spec.Retry != nil {
		retry := *spec.Retry
		spec.Retry = &retry
	}
	return spec
}

func cloneBinding(binding Binding) Binding {
	binding.BindingSpec = cloneBindingSpec(binding.BindingSpec)
	return binding
}

// NormalizeBindingSpecs applies kit defaults to specs and returns a slice of
// warnings describing any defaults that were applied. It mutates specs in
// place — callers pass a freshly built slice (the typical pattern).
//
// Currently:
//   - When Retry is nil AND WithoutRetry is false, [DefaultRetryPolicy] is
//     applied. Silent ack-and-discard on first transient error was the
//     previous default and is now opt-in via WithoutRetry. The warning lets
//     operators see, in the consumer's startup log, that the kit picked
//     the default for them.
func NormalizeBindingSpecs(specs []BindingSpec) []string {
	var warnings []string
	for i := range specs {
		if specs[i].Retry == nil && !specs[i].WithoutRetry {
			specs[i].Retry = DefaultRetryPolicy()
			warnings = append(warnings, fmt.Sprintf(
				"retry not configured and WithoutRetry not set; applied DefaultRetryPolicy (MaxRetries=%d, Delay=%s). Set WithoutRetry=true on the BindingSpec to keep ack-and-discard semantics.",
				specs[i].Retry.MaxRetries, specs[i].Retry.Delay,
			))
		}
	}
	return warnings
}

// ValidateBindingSpecs checks that all specs have valid exchange names, queue
// names, exchange types, routing keys, and retry policies.
//
// Callers that want kit defaults applied (Retry filling) should call
// [NormalizeBindingSpecs] *before* validation. [ComputeBindings] and the
// backend-specific DeclareAll do this for you.
func ValidateBindingSpecs(specs []BindingSpec) error {
	for _, b := range specs {
		if err := ValidateExchangeName(b.Exchange); err != nil {
			return err
		}
		if err := validateConsumerGroup(b.ConsumerGroup, b.Retry != nil); err != nil {
			return err
		}
		switch b.ExchangeType {
		case ExchangeDirect, ExchangeFanout, ExchangeTopic, ExchangeHeaders:
		default:
			return errors.New("unsupported exchange type")
		}
		if b.RoutingKey == "" && (b.ExchangeType == ExchangeDirect || b.ExchangeType == ExchangeTopic) {
			return fmt.Errorf("routing key required for %s exchange", b.ExchangeType)
		}
		if err := ValidateRoutingKey(b.RoutingKey); err != nil {
			return err
		}
		if b.Retry != nil && b.WithoutRetry {
			return errors.New("retry and WithoutRetry are mutually exclusive — set Retry to override the default policy, or set WithoutRetry=true for ack-and-discard semantics")
		}
		if b.Retry != nil {
			if b.Retry.MaxRetries < 1 {
				return errors.New("retry policy MaxRetries must be >= 1")
			}
			// Reject sub-millisecond delays. The amqpbackend topology emits
			// x-message-ttl as `int64(Delay / time.Millisecond)` which
			// truncates to 0 for sub-ms inputs — and a 0-TTL retry queue
			// re-delivers immediately, producing a tight loop on every
			// transient handler error.
			if b.Retry.Delay < time.Millisecond {
				return errors.New("retry policy Delay must be >= 1ms; sub-ms delays truncate to 0 in the AMQP TTL")
			}
		}
	}
	return nil
}

// retryQueueSuffix is the longest suffix ComputeBindings appends to a
// ConsumerGroup when retry topology is declared (".retry" is 6 bytes,
// longer than ".dead"). The derived queue name must stay within the
// portable route-name cap, so a consumer group is allowed at most
// MaxRouteNameBytes-len(retryQueueSuffix) bytes when retry is set.
const retryQueueSuffix = ".retry"

// validateConsumerGroup holds the ConsumerGroup to the same portable
// token rules as exchange names (non-empty, <= MaxRouteNameBytes, valid
// UTF-8, no control or whitespace bytes) so a non-portable value fails
// fast here rather than at broker-declaration time. ConsumerGroup is
// used verbatim as AMQP queue names and dead-letter routing keys, and
// when retry is configured it is also the stem for the ".retry"/".dead"
// queue names — so the effective length budget shrinks by the longest
// derived suffix when withRetry is true.
//
// Error messages intentionally do not echo the consumer-group value,
// matching the redaction posture of the route validators.
func validateConsumerGroup(consumerGroup string, withRetry bool) error {
	if err := ValidateRoutingKey(consumerGroup); err != nil {
		// Reuse the routing-key token rules (length, UTF-8, control,
		// whitespace) but re-label the field and keep the non-empty
		// message wording stable for existing callers/tests.
		return fmt.Errorf("consumer group invalid: %w", ErrInvalidRoute)
	}
	if consumerGroup == "" {
		return errors.New("consumer group must not be empty")
	}
	if withRetry && len(consumerGroup) > MaxRouteNameBytes-len(retryQueueSuffix) {
		return errors.New("consumer group too long: derived retry/dead queue name would exceed the portable route-name limit")
	}
	return nil
}

// ComputeBindings converts BindingSpecs into Bindings by computing the
// retry/dead exchange and queue names. Unlike backend-specific DeclareAll,
// it requires no broker connection — it is a pure function. Consumer services
// use this to obtain Binding objects without declaring topology themselves.
//
// The returned bindings own detached retry-policy copies so caller mutation
// after setup cannot alter consumer retry decisions.
func ComputeBindings(specs ...BindingSpec) ([]Binding, error) {
	specs = CloneBindingSpecs(specs)
	_ = NormalizeBindingSpecs(specs)
	if err := ValidateBindingSpecs(specs); err != nil {
		return nil, err
	}

	result := make([]Binding, 0, len(specs))
	for _, b := range specs {
		db := Binding{BindingSpec: b}
		if b.Retry != nil {
			db.RetryExchange = b.Exchange + ".retry"
			db.RetryQueue = b.ConsumerGroup + ".retry"
			db.DeadExchange = DeadExchangeName(b.Exchange)
			db.DeadQueue = b.ConsumerGroup + ".dead"
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
			return cloneBinding(b), nil
		}
	}
	return Binding{}, errors.New("no binding found for routing key")
}

// DeadExchangeName returns the conventional dead-letter exchange name.
func DeadExchangeName(exchange string) string { return exchange + ".dead" }
