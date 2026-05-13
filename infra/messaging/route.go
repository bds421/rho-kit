package messaging

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

const (
	// MaxRouteNameBytes is the portable AMQP shortstr limit used for exchange
	// names and routing keys. Redis and NATS can accept other shapes, but the
	// shared publisher contract stays within the narrowest backend boundary.
	MaxRouteNameBytes = 255
)

// ErrInvalidRoute marks an exchange name or routing key that is not portable
// across the kit's AMQP, NATS, Redis, buffered, and in-memory publishers. It
// is an [apperror.ValidationError] so HTTP and gRPC adapters map it to
// 400/InvalidArgument automatically.
var ErrInvalidRoute = apperror.NewValidation("messaging: invalid route")

// ValidatePublishRoute checks the shared publisher route contract. The
// exchange is required; routingKey may be empty for fanout/exchange-only
// publishes.
func ValidatePublishRoute(exchange, routingKey string) error {
	if err := ValidateExchangeName(exchange); err != nil {
		return err
	}
	if err := ValidateRoutingKey(routingKey); err != nil {
		return err
	}
	return nil
}

// ValidateExchangeName rejects exchange names that cannot safely be used as
// backend names, log fields, metric labels, and in-memory matcher values.
func ValidateExchangeName(exchange string) error {
	return validateRoutePart("exchange name", exchange, false)
}

// ValidateRoutingKey rejects routing keys that cannot safely be used across
// every publisher backend. An empty routing key is valid for fanout-style
// routes.
func ValidateRoutingKey(routingKey string) error {
	return validateRoutePart("routing key", routingKey, true)
}

func validateRoutePart(kind, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%w: %s must not be empty", ErrInvalidRoute, kind)
	}
	if len(value) > MaxRouteNameBytes {
		return fmt.Errorf("%w: %s exceeds maximum length", ErrInvalidRoute, kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s contains invalid UTF-8", ErrInvalidRoute, kind)
	}
	if strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalidRoute, kind)
	}
	return nil
}
