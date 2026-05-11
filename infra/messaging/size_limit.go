package messaging

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DefaultMaxMessageBytes is the cross-backend publish limit used by
// messaging publishers unless callers configure a stricter or looser limit.
const DefaultMaxMessageBytes = 1 << 20 // 1 MiB

// ErrMessageTooLarge marks publish attempts rejected before they reach the
// broker because the serialized message exceeds the configured size policy.
var ErrMessageTooLarge = errors.New("messaging: message exceeds max size")

// MessageTooLargeError reports the measured message size and the effective
// limit that rejected it.
type MessageTooLargeError struct {
	Exchange   string
	RoutingKey string
	Size       int
	Limit      int
}

func (e *MessageTooLargeError) Error() string {
	return fmt.Sprintf("messaging: message size %d exceeds max %d", e.Size, e.Limit)
}

func (e *MessageTooLargeError) Unwrap() error { return ErrMessageTooLarge }

// MessageSizeRouteLimit overrides the default max size for one exact
// exchange+routing-key pair. RoutingKey may be empty for fanout-style routes.
type MessageSizeRouteLimit struct {
	Exchange   string
	RoutingKey string
	MaxBytes   int
}

type messageSizeRoute struct {
	exchange   string
	routingKey string
}

// MessageSizeLimiter applies a default max serialized message size plus
// optional exact route overrides. The zero value is the safe default
// ([DefaultMaxMessageBytes]), so backends cannot forget to opt in.
type MessageSizeLimiter struct {
	configured      bool
	defaultMaxBytes int
	routeMaxBytes   map[messageSizeRoute]int
}

// DefaultMessageSizeLimiter returns the safe cross-backend default limiter.
func DefaultMessageSizeLimiter() MessageSizeLimiter {
	return NewMessageSizeLimiter(DefaultMaxMessageBytes)
}

// UnlimitedMessageSizeLimiter returns an explicit no-limit policy. Use only
// when an outer protocol, broker, or product-level contract already bounds
// message size.
func UnlimitedMessageSizeLimiter() MessageSizeLimiter {
	return MessageSizeLimiter{configured: true}
}

// NewMessageSizeLimiter creates a limiter with defaultMaxBytes. A zero default
// disables the default limit; use route overrides to cap selected routes.
func NewMessageSizeLimiter(defaultMaxBytes int, overrides ...MessageSizeRouteLimit) MessageSizeLimiter {
	if defaultMaxBytes < 0 {
		panic("messaging: NewMessageSizeLimiter requires defaultMaxBytes >= 0")
	}
	l := MessageSizeLimiter{
		configured:      true,
		defaultMaxBytes: defaultMaxBytes,
	}
	for _, override := range overrides {
		l = l.WithRouteMaxBytes(override.Exchange, override.RoutingKey, override.MaxBytes)
	}
	return l
}

// WithDefaultMaxBytes returns a copy with a new default limit.
func (l MessageSizeLimiter) WithDefaultMaxBytes(maxBytes int) MessageSizeLimiter {
	if maxBytes <= 0 {
		panic("messaging: WithDefaultMaxBytes requires maxBytes > 0")
	}
	l = l.normalized()
	l.defaultMaxBytes = maxBytes
	return l
}

// WithoutDefaultMaxBytes returns a copy with no default limit. Existing route
// overrides continue to apply.
func (l MessageSizeLimiter) WithoutDefaultMaxBytes() MessageSizeLimiter {
	l = l.normalized()
	l.defaultMaxBytes = 0
	return l
}

// WithRouteMaxBytes returns a copy with a route-specific limit. Exchange must
// be non-empty; RoutingKey may be empty for fanout-style routes.
func (l MessageSizeLimiter) WithRouteMaxBytes(exchange, routingKey string, maxBytes int) MessageSizeLimiter {
	if err := ValidatePublishRoute(exchange, routingKey); err != nil {
		panic("messaging: WithRouteMaxBytes invalid route")
	}
	if maxBytes <= 0 {
		panic("messaging: WithRouteMaxBytes requires maxBytes > 0")
	}
	l = l.normalized()
	if l.routeMaxBytes == nil {
		l.routeMaxBytes = make(map[messageSizeRoute]int, 1)
	} else {
		clone := make(map[messageSizeRoute]int, len(l.routeMaxBytes)+1)
		for k, v := range l.routeMaxBytes {
			clone[k] = v
		}
		l.routeMaxBytes = clone
	}
	l.routeMaxBytes[messageSizeRoute{exchange: exchange, routingKey: routingKey}] = maxBytes
	return l
}

// LimitFor returns the effective max bytes for a route. A zero return means no
// limit applies.
func (l MessageSizeLimiter) LimitFor(exchange, routingKey string) int {
	l = l.normalized()
	if maxBytes, ok := l.routeMaxBytes[messageSizeRoute{exchange: exchange, routingKey: routingKey}]; ok {
		return maxBytes
	}
	return l.defaultMaxBytes
}

// Check rejects msg when its estimated wire size exceeds the route's limit.
func (l MessageSizeLimiter) Check(exchange, routingKey string, msg Message) error {
	limit := l.LimitFor(exchange, routingKey)
	if limit == 0 {
		return nil
	}
	size, err := EstimateMessageBytes(msg)
	if err != nil {
		return err
	}
	if size > limit {
		return &MessageTooLargeError{
			Exchange:   exchange,
			RoutingKey: routingKey,
			Size:       size,
			Limit:      limit,
		}
	}
	return nil
}

// EstimateMessageBytes returns the cross-backend size estimate used by
// MessageSizeLimiter. It includes the JSON message body plus transport headers,
// because headers ride outside Message's JSON body on AMQP, NATS, and Redis
// Streams but still consume broker frame/storage budget.
func EstimateMessageBytes(msg Message) (int, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("messaging: estimate message size: %w", err)
	}
	size := len(body)
	for k, v := range msg.Headers {
		size += len(k) + len(v)
	}
	return size, nil
}

func (l MessageSizeLimiter) normalized() MessageSizeLimiter {
	if l.configured {
		return l
	}
	return DefaultMessageSizeLimiter()
}
