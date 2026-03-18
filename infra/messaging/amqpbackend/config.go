package amqpbackend

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"

	"github.com/bds421/rho-kit/core/config"
)

// RabbitMQConfig holds AMQP connection settings.
//
// Configure via URL directly, or via individual fields (Host, Port, User,
// Password, VHost) which are assembled into an AMQP URL. When URL is non-empty
// it takes precedence over individual fields.
type RabbitMQConfig struct {
	URL      string
	Host     string
	Port     int
	User     string
	Password string
	VHost    string
}

// AMQPURL returns the resolved AMQP connection URL. If URL is set directly,
// it is returned as-is. Otherwise, the URL is built from individual fields.
// Returns an empty string if neither URL nor Host is configured.
func (c RabbitMQConfig) AMQPURL() string {
	if c.URL != "" {
		return c.URL
	}
	if c.Host == "" {
		return ""
	}
	port := c.Port
	if port == 0 {
		port = 5672
	}
	vhost := c.VHost
	if vhost == "" {
		vhost = "/"
	}
	u := &url.URL{
		Scheme: "amqp",
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(port)),
		Path:   vhost,
	}
	if c.User != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	return u.String()
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials
// embedded in the AMQP URL.
func (c RabbitMQConfig) LogValue() slog.Value {
	resolved := c.AMQPURL()
	if resolved == "" {
		return slog.StringValue("[NOT CONFIGURED]")
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return slog.StringValue("[INVALID URL]")
	}
	return slog.StringValue(u.Redacted())
}

// RabbitMQFields holds RabbitMQ connection configuration.
// Embed this in service configs that use RabbitMQ.
type RabbitMQFields struct {
	RabbitMQ RabbitMQConfig
}

// LoadRabbitMQFields reads the RabbitMQ connection config from environment variables.
//
// If RABBITMQ_URL is set, it is used directly. Otherwise, the connection is
// built from individual fields:
//   - RABBITMQ_HOST (required when no URL)
//   - RABBITMQ_PORT (default: 5672)
//   - RABBITMQ_USER (default: guest)
//   - RABBITMQ_PASSWORD (secret, default: guest)
//   - RABBITMQ_VHOST (default: /)
func LoadRabbitMQFields() (RabbitMQFields, error) {
	// RABBITMQ_URL takes precedence.
	if rawURL := config.GetSecret("RABBITMQ_URL", ""); rawURL != "" {
		return RabbitMQFields{
			RabbitMQ: RabbitMQConfig{URL: rawURL},
		}, nil
	}

	// Fallback: individual env vars.
	p := &config.Parser{}
	port := p.Int("RABBITMQ_PORT", 5672)
	if err := p.Err(); err != nil {
		return RabbitMQFields{}, err
	}

	return RabbitMQFields{
		RabbitMQ: RabbitMQConfig{
			Host:     config.Get("RABBITMQ_HOST", ""),
			Port:     port,
			User:     config.Get("RABBITMQ_USER", "guest"),
			Password: config.GetSecret("RABBITMQ_PASSWORD", "guest"),
			VHost:    config.Get("RABBITMQ_VHOST", "/"),
		},
	}, nil
}

// ValidateRabbitMQ checks the AMQP URL format and credential strength.
func (f RabbitMQFields) ValidateRabbitMQ(environment string) error {
	resolved := f.RabbitMQ.AMQPURL()
	if err := ValidateAMQPURL("RABBITMQ_URL", resolved); err != nil {
		return err
	}
	if !config.IsDevelopment(environment) {
		if err := config.RejectWeakCredential("RABBITMQ_PASSWORD", resolved); err != nil {
			return fmt.Errorf("%w (environment: %s)", err, environment)
		}
	}
	return nil
}

// ValidateAMQPURL checks that rawURL is a non-empty, parseable URL with an
// amqp or amqps scheme. name is used in error messages (e.g. "RABBITMQ_URL").
func ValidateAMQPURL(name, rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		return fmt.Errorf("%s scheme must be amqp or amqps, got %q", name, u.Scheme)
	}
	return nil
}
