package amqpbackend

import (
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = Config{}

func TestConfig_LogValue_Redacts(t *testing.T) {
	cfg := Config{URL: "amqp://token-user:rabbit-secret@tenant-rabbit.internal:5672/tenant-vhost?token=query-secret#frag"}
	val := cfg.LogValue()
	s := val.String()
	assert.NotContains(t, s, "token-user")
	assert.NotContains(t, s, "rabbit-secret")
	assert.NotContains(t, s, "query-secret")
	assert.NotContains(t, s, "tenant-rabbit.internal")
	assert.NotContains(t, s, "tenant-vhost")
	assert.Contains(t, s, "url_configured=true")
	assert.Contains(t, s, "url_valid=true")
	assert.Contains(t, s, "host_configured=true")
	assert.Contains(t, s, "user_configured=true")
	assert.Contains(t, s, "password_configured=true")
	assert.Contains(t, s, "vhost_configured=true")
}

func TestConfig_LogValue_InvalidURL(t *testing.T) {
	cfg := Config{URL: "://invalid"}
	val := cfg.LogValue()
	assert.Contains(t, val.String(), "url_valid=false")
}

func TestConfig_LogValue_NotConfigured(t *testing.T) {
	cfg := Config{}
	assert.Contains(t, cfg.LogValue().String(), "url_configured=false")
}

func TestConfig_AMQPURL_FromURL(t *testing.T) {
	cfg := Config{URL: "amqp://user:pass@host:5672/vhost"}
	assert.Equal(t, "amqp://user:pass@host:5672/vhost", cfg.AMQPURL())
}

func TestConfig_AMQPURL_URLTakesPrecedence(t *testing.T) {
	cfg := Config{
		URL:  "amqp://url-user:pass@url-host:5672/",
		Host: "field-host",
		User: "field-user",
	}
	assert.Equal(t, "amqp://url-user:pass@url-host:5672/", cfg.AMQPURL())
}

func TestConfig_AMQPURL_FromFields(t *testing.T) {
	cfg := Config{
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

func TestConfig_AMQPURL_DefaultPort(t *testing.T) {
	cfg := Config{Host: "rabbit", User: "u", Password: "p"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	assert.Equal(t, "5672", parsed.Port())
}

func TestConfig_AMQPURL_DefaultVHost(t *testing.T) {
	cfg := Config{Host: "rabbit", User: "u", Password: "p"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	assert.Equal(t, "/", parsed.Path)
}

func TestConfig_AMQPURL_SpecialCharsInPassword(t *testing.T) {
	cfg := Config{Host: "h", Port: 5672, User: "u", Password: "p@ss/word"}
	parsed, err := url.Parse(cfg.AMQPURL())
	require.NoError(t, err)
	pw, _ := parsed.User.Password()
	assert.Equal(t, "p@ss/word", pw)
}

func TestConfig_AMQPURL_EmptyHost(t *testing.T) {
	cfg := Config{}
	assert.Empty(t, cfg.AMQPURL())
}

func TestLoadFields_URL(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqps://user:pass@rabbitmq:5671/")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "amqps://user:pass@rabbitmq:5671/", f.RabbitMQ.AMQPURL())
}

func TestLoadFields_IndividualFields(t *testing.T) {
	t.Setenv("RABBITMQ_HOST", "my-rabbit")
	t.Setenv("RABBITMQ_PORT", "5673")
	t.Setenv("RABBITMQ_USER", "admin")
	t.Setenv("RABBITMQ_PASSWORD", "strongpass")
	t.Setenv("RABBITMQ_VHOST", "/myapp")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "my-rabbit", f.RabbitMQ.Host)
	assert.Equal(t, 5673, f.RabbitMQ.Port)
	assert.Equal(t, "admin", f.RabbitMQ.User)
	assert.Contains(t, f.RabbitMQ.AMQPURL(), "my-rabbit:5673")
	assert.Contains(t, f.RabbitMQ.AMQPURL(), "/myapp")
}

func TestLoadFields_URLPrecedence(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://url-user:pass@url-host:5672/")
	t.Setenv("RABBITMQ_HOST", "field-host")
	t.Setenv("RABBITMQ_USER", "field-user")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "amqp://url-user:pass@url-host:5672/", f.RabbitMQ.AMQPURL())
}

func TestLoadFields_Defaults(t *testing.T) {
	t.Setenv("RABBITMQ_HOST", "rabbit")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, 5672, f.RabbitMQ.Port)
	// Credentials default empty — guest/guest is never implied silently.
	assert.Equal(t, "", f.RabbitMQ.User)
	assert.Equal(t, "", f.RabbitMQ.Password)
	assert.Equal(t, "/", f.RabbitMQ.VHost)
}

func TestFields_ValidateRabbitMQ(t *testing.T) {
	// strong password — at least 12 chars, no obvious weak markers.
	const strongPW = "S3cur3-P4ssw0rd!9KJZ"

	t.Run("valid URL with strong password", func(t *testing.T) {
		f := Fields{RabbitMQ: Config{URL: "amqps://user:" + strongPW + "@rabbitmq:5671/"}}
		assert.NoError(t, f.ValidateRabbitMQ(""))
	})

	t.Run("valid fields with strong password", func(t *testing.T) {
		f := Fields{RabbitMQ: Config{Host: "rabbit", User: "u", Password: strongPW}}
		assert.NoError(t, f.ValidateRabbitMQ(""))
	})

	t.Run("empty", func(t *testing.T) {
		f := Fields{RabbitMQ: Config{}}
		assert.Error(t, f.ValidateRabbitMQ(""))
	})

	// Regression test for audit finding N-5: the previous version
	// passed the entire URL string to RejectWeakCredential, so
	// "amqp://guest:guest@..." passed because the URL is longer than
	// the 12-char threshold. After the fix, the password component is
	// extracted and checked.
	t.Run("default guest password rejected", func(t *testing.T) {
		f := Fields{RabbitMQ: Config{URL: "amqp://guest:guest@host:5672/"}}
		err := f.ValidateRabbitMQ("")
		assert.Error(t, err, "default guest:guest credentials must be rejected")
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
		{"missing host", "amqps:///vhost", true},
		{"empty hostname", "amqps://user:pass@:5671/vhost", true},
		{"empty port", "amqps://user:pass@host:/vhost", true},
		{"zero port", "amqps://user:pass@host:0/vhost", true},
		{"too large port", "amqps://user:pass@host:65536/vhost", true},
		{"zone identifier", "amqps://user:pass@[fe80::1%25lo0]:5671/vhost", true},
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

func TestValidateAMQPURL_ParseErrorDoesNotEchoValue(t *testing.T) {
	err := ValidateAMQPURL("TEST_URL", "amqps://rabbit/%zz?token=secret-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_URL is invalid")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestValidateAMQPURL_SchemeErrorDoesNotEchoValue(t *testing.T) {
	err := ValidateAMQPURL("TEST_URL", "secret-token://rabbit")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_URL scheme must be amqp or amqps")
	assert.NotContains(t, err.Error(), "secret-token")
}
