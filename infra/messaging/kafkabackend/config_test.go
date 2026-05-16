package kafkabackend

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig_RejectsEmptyBrokers(t *testing.T) {
	err := ValidateConfig(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Brokers must not be empty")
}

func TestValidateConfig_RejectsBlankBroker(t *testing.T) {
	err := ValidateConfig(Config{Brokers: []string{"   "}, AllowInsecure: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be blank")
}

func TestValidateConfig_RejectsPlaintextNoAuth(t *testing.T) {
	err := ValidateConfig(Config{Brokers: []string{"localhost:9092"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FR-073")
}

func TestValidateConfig_AllowsTLS(t *testing.T) {
	err := ValidateConfig(Config{
		Brokers: []string{"localhost:9092"},
		TLS:     &tls.Config{MinVersion: tls.VersionTLS12},
	})
	assert.NoError(t, err)
}

func TestValidateConfig_AllowsSASL(t *testing.T) {
	err := ValidateConfig(Config{
		Brokers:       []string{"localhost:9092"},
		SASLMechanism: "PLAIN",
		SASLUsername:  "u",
		SASLPassword:  "p",
	})
	assert.NoError(t, err)
}

func TestValidateConfig_AllowsExplicitInsecure(t *testing.T) {
	err := ValidateConfig(Config{
		Brokers:       []string{"localhost:9092"},
		AllowInsecure: true,
	})
	assert.NoError(t, err)
}

func TestValidateConfig_RejectsUnsupportedSASL(t *testing.T) {
	err := ValidateConfig(Config{
		Brokers:       []string{"localhost:9092"},
		SASLMechanism: "OAUTHBEARER",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported SASL mechanism")
}

func TestValidateConfig_SASLRequiresCredentials(t *testing.T) {
	err := ValidateConfig(Config{
		Brokers:       []string{"localhost:9092"},
		SASLMechanism: "PLAIN",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SASLUsername")
}

func TestConfig_Clone_DetachesSlice(t *testing.T) {
	cfg := Config{Brokers: []string{"a:9092", "b:9092"}, AllowInsecure: true}
	clone, err := cfg.Clone()
	require.NoError(t, err)
	cfg.Brokers[0] = "MUTATED"
	assert.Equal(t, "a:9092", clone.Brokers[0])
}

func TestConfig_Clone_RaisesTLSFloor(t *testing.T) {
	cfg := Config{
		Brokers: []string{"a:9092"},
		TLS:     &tls.Config{MinVersion: tls.VersionTLS10},
	}
	clone, err := cfg.Clone()
	require.NoError(t, err)
	require.NotNil(t, clone.TLS)
	assert.GreaterOrEqual(t, int(clone.TLS.MinVersion), int(tls.VersionTLS12))
}

func TestConfig_LogValue_ShapeOnly(t *testing.T) {
	cfg := Config{
		Brokers:       []string{"a:9092", "b:9092"},
		SASLMechanism: "PLAIN",
		SASLUsername:  "secret-user",
		SASLPassword:  "secret-password",
	}
	rendered := cfg.LogValue().String()
	assert.NotContains(t, rendered, "secret-user")
	assert.NotContains(t, rendered, "secret-password")
	assert.NotContains(t, rendered, "a:9092")
}
