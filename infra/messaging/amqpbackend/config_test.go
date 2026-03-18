package amqpbackend

import (
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = RabbitMQConfig{}

func TestRabbitMQConfig_LogValue_Redacts(t *testing.T) {
	cfg := RabbitMQConfig{URL: "amqp://user:pass@localhost:5672/vhost"}
	val := cfg.LogValue()
	s := val.String()
	assert.NotContains(t, s, "pass")
	assert.Contains(t, s, "localhost")
}

func TestRabbitMQConfig_LogValue_InvalidURL(t *testing.T) {
	cfg := RabbitMQConfig{URL: "://invalid"}
	val := cfg.LogValue()
	assert.Equal(t, "[INVALID URL]", val.String())
}

func TestRabbitMQConfig_LogValue_NotConfigured(t *testing.T) {
	cfg := RabbitMQConfig{}
	assert.Equal(t, "[NOT CONFIGURED]", cfg.LogValue().String())
}

func TestRabbitMQConfig_AMQPURL_FromURL(t *testing.T) {
	cfg := RabbitMQConfig{URL: "amqp://user:pass@host:5672/vhost"}
	assert.Equal(t, "amqp://user:pass@host:5672/vhost", cfg.AMQPURL())
}

func TestRabbitMQConfig_AMQPURL_URLTakesPrecedence(t *testing.T) {
	cfg := RabbitMQConfig{
		URL:  "amqp://url-user:pass@url-host:5672/",
		Host: "field-host",
		User: "field-user",
	}
	assert.Equal(t, "amqp://url-user:pass@url-host:5672/", cfg.AMQPURL())
}

func TestRabbitMQConfig_AMQPURL_FromFields(t *testing.T) {
	cfg := RabbitMQConfig{
		Host:     "rabbit",
		Port:     5672,
		User:     "admin",
		Password: "secret",
		VHost:    "/myapp",
	}
	u := cfg.AMQPURL()
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, "amqp", parsed.Scheme)
	assert.Equal(t, "rabbit", parsed.Hostname())
	assert.Equal(t, "5672", parsed.Port())
	assert.Equal(t, "admin", parsed.User.Username())
	pw, _ := parsed.User.Password()
	assert.Equal(t, "secret", pw)
	assert.Equal(t, "/myapp", parsed.Path)
}

func TestRabbitMQConfig_AMQPURL_DefaultPort(t *testing.T) {
	cfg := RabbitMQConfig{Host: "rabbit", User: "u", Password: "p"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	assert.Equal(t, "5672", parsed.Port())
}

func TestRabbitMQConfig_AMQPURL_DefaultVHost(t *testing.T) {
	cfg := RabbitMQConfig{Host: "rabbit", User: "u", Password: "p"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	assert.Equal(t, "/", parsed.Path)
}

func TestRabbitMQConfig_AMQPURL_SpecialCharsInPassword(t *testing.T) {
	cfg := RabbitMQConfig{Host: "h", Port: 5672, User: "u", Password: "p@ss/word"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	pw, _ := parsed.User.Password()
	assert.Equal(t, "p@ss/word", pw)
}

func TestRabbitMQConfig_AMQPURL_EmptyHost(t *testing.T) {
	cfg := RabbitMQConfig{}
	assert.Empty(t, cfg.AMQPURL())
}

func TestLoadRabbitMQFields_URL(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqps://user:pass@rabbitmq:5671/")

	f, err := LoadRabbitMQFields()
	require.NoError(t, err)
	assert.Equal(t, "amqps://user:pass@rabbitmq:5671/", f.RabbitMQ.AMQPURL())
}

func TestLoadRabbitMQFields_IndividualFields(t *testing.T) {
	t.Setenv("RABBITMQ_HOST", "my-rabbit")
	t.Setenv("RABBITMQ_PORT", "5673")
	t.Setenv("RABBITMQ_USER", "admin")
	t.Setenv("RABBITMQ_PASSWORD", "strongpass")
	t.Setenv("RABBITMQ_VHOST", "/myapp")

	f, err := LoadRabbitMQFields()
	require.NoError(t, err)
	assert.Equal(t, "my-rabbit", f.RabbitMQ.Host)
	assert.Equal(t, 5673, f.RabbitMQ.Port)
	assert.Equal(t, "admin", f.RabbitMQ.User)
	assert.Contains(t, f.RabbitMQ.AMQPURL(), "my-rabbit:5673")
	assert.Contains(t, f.RabbitMQ.AMQPURL(), "/myapp")
}

func TestLoadRabbitMQFields_URLPrecedence(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://url-user:pass@url-host:5672/")
	t.Setenv("RABBITMQ_HOST", "field-host")
	t.Setenv("RABBITMQ_USER", "field-user")

	f, err := LoadRabbitMQFields()
	require.NoError(t, err)
	assert.Equal(t, "amqp://url-user:pass@url-host:5672/", f.RabbitMQ.AMQPURL())
}

func TestLoadRabbitMQFields_Defaults(t *testing.T) {
	t.Setenv("RABBITMQ_HOST", "rabbit")

	f, err := LoadRabbitMQFields()
	require.NoError(t, err)
	assert.Equal(t, 5672, f.RabbitMQ.Port)
	assert.Equal(t, "guest", f.RabbitMQ.User)
	assert.Equal(t, "/", f.RabbitMQ.VHost)
}

func TestRabbitMQFields_ValidateRabbitMQ(t *testing.T) {
	t.Run("valid URL", func(t *testing.T) {
		f := RabbitMQFields{RabbitMQ: RabbitMQConfig{URL: "amqps://user:strongpass@rabbitmq:5671/"}}
		assert.NoError(t, f.ValidateRabbitMQ("development"))
	})

	t.Run("valid fields", func(t *testing.T) {
		f := RabbitMQFields{RabbitMQ: RabbitMQConfig{Host: "rabbit", User: "u", Password: "p"}}
		assert.NoError(t, f.ValidateRabbitMQ("development"))
	})

	t.Run("empty", func(t *testing.T) {
		f := RabbitMQFields{RabbitMQ: RabbitMQConfig{}}
		assert.Error(t, f.ValidateRabbitMQ("development"))
	})
}

func TestValidateAMQPURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid amqp", "amqp://user:pass@localhost:5672/", false},
		{"valid amqps", "amqps://user:pass@host:5671/", false},
		{"empty", "", true},
		{"wrong scheme http", "http://localhost:5672/", true},
		{"wrong scheme ftp", "ftp://localhost/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAMQPURL("TEST_URL", tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
