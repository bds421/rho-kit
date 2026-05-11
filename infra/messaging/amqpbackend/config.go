package amqpbackend

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"

	"github.com/bds421/rho-kit/core/v2/config"
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
// or topology embedded in the AMQP URL.
func (c RabbitMQConfig) LogValue() slog.Value {
	urlValid, urlHostConfigured, urlUserConfigured, urlPasswordConfigured, urlVHostConfigured := amqpURLLogState(c.URL)
	return slog.GroupValue(
		slog.Bool("url_configured", c.URL != ""),
		slog.Bool("url_valid", urlValid),
		slog.Bool("host_configured", c.Host != "" || urlHostConfigured),
		slog.Int("port", c.Port),
		slog.Bool("user_configured", c.User != "" || urlUserConfigured),
		slog.Bool("password_configured", c.Password != "" || urlPasswordConfigured),
		slog.Bool("vhost_configured", c.VHost != "" || urlVHostConfigured),
	)
}

func amqpURLLogState(rawURL string) (valid, hostConfigured, userConfigured, passwordConfigured, vhostConfigured bool) {
	if rawURL == "" {
		return true, false, false, false, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, false, false, false, false
	}
	passwordConfigured = false
	if u.User != nil {
		_, passwordConfigured = u.User.Password()
	}
	return true, u.Host != "", u.User != nil && u.User.Username() != "", passwordConfigured, u.EscapedPath() != ""
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
	if rawURL := config.MustGetSecret("RABBITMQ_URL", ""); rawURL != "" {
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
			Password: config.MustGetSecret("RABBITMQ_PASSWORD", "guest"),
			VHost:    config.Get("RABBITMQ_VHOST", "/"),
		},
	}, nil
}

// ValidateRabbitMQ checks the AMQP URL format and credential strength.
//
// The environment parameter is preserved for API compatibility but is
// no longer consulted — the kit's "no development mode" policy means
// credential-strength checks fire unconditionally.
//
// The password is extracted from the resolved AMQP URL (URL.User) and
// passed to RejectWeakCredential. The previous version passed the
// entire URL string, which silently accepted "amqp://guest:guest@..."
// because the URL is longer than the weak-credential length cap and
// does not contain "changeme" — defeating the check (audit finding
// N-5).
func (f RabbitMQFields) ValidateRabbitMQ(environment string) error {
	_ = environment // accepted for API compatibility; no longer consulted
	resolved := f.RabbitMQ.AMQPURL()
	if err := ValidateAMQPURL("RABBITMQ_URL", resolved); err != nil {
		return err
	}
	password := extractAMQPPassword(resolved)
	if err := config.RejectWeakCredential("RABBITMQ_PASSWORD", password); err != nil {
		return err
	}
	return nil
}

// extractAMQPPassword returns the password component of an AMQP URL,
// or the empty string if the URL has no userinfo or cannot be parsed.
// An empty result causes RejectWeakCredential to fire its
// "must not be empty" branch — desirable, since an AMQP URL without a
// password is itself a misconfiguration in production.
func extractAMQPPassword(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return ""
	}
	pw, _ := u.User.Password()
	return pw
}

// ValidateAMQPURL checks that rawURL is a non-empty, parseable URL with an
// amqp or amqps scheme and a network host. name is used in error messages
// (e.g. "RABBITMQ_URL").
func ValidateAMQPURL(name, rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s is invalid", name)
	}
	if u.Scheme != "amqp" && u.Scheme != "amqps" {
		return fmt.Errorf("%s scheme must be amqp or amqps", name)
	}
	if err := config.ValidateURLHost(name, u); err != nil {
		return err
	}
	return nil
}
